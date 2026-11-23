package backend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

var mofaHTTPClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

var errMofaResultUnknown = errors.New("MOFA 尚未确认本次写入结果，请在后台核对后重试")

type mofaClient struct {
	site string
	jwt  string
	http *http.Client
}

type mofaLoginResult struct {
	AccessToken string `json:"access_token"`
	Requires2FA bool   `json:"requires_2fa"`
	TempToken   string `json:"temp_token"`
	User        struct {
		Role string `json:"role"`
	} `json:"user"`
}

type mofaImportResult struct {
	Status           string `json:"status"`
	Action           string `json:"action"`
	AccountID        int64  `json:"account_id"`
	GroupID          int64  `json:"group_id"`
	AuthorizationURL string `json:"authorization_url"`
}

type mofaHTTPError struct {
	status int
	code   string
}

func (e *mofaHTTPError) Error() string { return fmt.Sprintf("MOFA 返回 HTTP %d", e.status) }

func (c *mofaClient) post(ctx context.Context, path string, input map[string]string, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return errors.New("无法创建 MOFA 请求")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.site+"/api/v1"+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("MOFA 后台地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.site)
	if c.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+c.jwt)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("无法连接 MOFA 后台，请检查网络或稍后重试")
	}
	defer resp.Body.Close()
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if resp.StatusCode != http.StatusOK {
		var failure struct{ Error string }
		_ = json.Unmarshal(body, &failure)
		return &mofaHTTPError{status: resp.StatusCode, code: failure.Error}
	}
	if err != nil || len(body) > maxBody || json.Unmarshal(body, output) != nil {
		return errors.New("MOFA 后台返回了无法读取的结果")
	}
	return nil
}

func (c *mofaClient) login(ctx context.Context, settings mofaImportSettings) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	login := func(path string, fields map[string]string, message string) (mofaLoginResult, error) {
		requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var response struct {
			Code *int            `json:"code"`
			Data mofaLoginResult `json:"data"`
		}
		if err := c.post(requestCtx, path, fields, &response); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return mofaLoginResult{}, errors.New("MOFA 管理员登录请求超时，请检查网络后重试")
			}
			var status *mofaHTTPError
			if !errors.As(err, &status) || status.status >= 500 || status.status == http.StatusTooManyRequests {
				return mofaLoginResult{}, err
			}
			return mofaLoginResult{}, errors.New(message)
		}
		if response.Code == nil || *response.Code != 0 {
			return mofaLoginResult{}, errors.New(message)
		}
		return response.Data, nil
	}
	result, err := login("/auth/login", map[string]string{"email": settings.Username, "password": settings.Password},
		"MOFA 管理员登录失败，请核对用户名、密码及后台的登录验证设置")
	if err != nil {
		return err
	}
	if result.Requires2FA {
		if result.TempToken == "" {
			return errors.New("MOFA 后台未返回有效的 2FA 登录会话")
		}
		if settings.TOTPSecret == "" {
			return errors.New("MOFA 管理员已启用 2FA，请在基础设置填写 2FA 种子")
		}
		// Use the next window when only a few seconds of this code remain.
		now := time.Now()
		if elapsed := now.Unix() % 30; elapsed >= 27 {
			timer := time.NewTimer(time.Duration(30-elapsed) * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				now = time.Now()
			}
		}
		code, err := mofaTOTP(settings.TOTPSecret, now)
		if err != nil {
			return err
		}
		result, err = login("/auth/login/2fa", map[string]string{"temp_token": result.TempToken, "totp_code": code},
			"MOFA 的 2FA 验证失败，请核对种子和系统时间")
		if err != nil {
			return err
		}
	}
	if result.AccessToken == "" || result.User.Role != "admin" {
		return errors.New("MOFA 登录成功后未取得管理员会话，请核对账号的权限")
	}
	c.jwt = result.AccessToken
	return nil
}

func mofaTOTP(seed string, now time.Time) (string, error) {
	seed = strings.ToUpper(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, seed))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(seed, "="))
	if err != nil || len(key) == 0 {
		return "", errors.New("MOFA 的 2FA 种子应为 Base32 格式")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	code := (binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%06d", code), nil
}

func (c *mofaClient) importRequest(ctx context.Context, job, phase string, fields map[string]string) (mofaImportResult, error) {
	if err := ctx.Err(); err != nil {
		return mofaImportResult{}, err
	}
	// Once sent, the server may commit membership or exchange a code even
	// after the browser job is cancelled. Wait for that write's acknowledgement.
	timeout := 20 * time.Second
	if phase == "complete" {
		timeout = 95 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	var result mofaImportResult
	err := c.post(requestCtx, "/admin/ant-accounts/imports/"+job+"/"+phase, fields, &result)
	if err != nil {
		var status *mofaHTTPError
		if !errors.As(err, &status) || (status.status >= 500 && status.code == "") {
			return mofaImportResult{}, errMofaResultUnknown
		}
	}
	return result, err
}

func mofaImportError(err error) error {
	var status *mofaHTTPError
	if !errors.As(err, &status) {
		return err
	}
	switch status.code {
	case "oauth_not_configured":
		return errors.New("MOFA 尚未配置此站点的 OpenAI OAuth 客户端和回调地址")
	case "identity_conflict", "identity_mismatch", "identity_unverified", "job_email_conflict":
		return errors.New("MOFA 无法确认本次授权与现有账号是同一身份，请核对授权页的邮箱和工作空间")
	case "target_group_unavailable", "target_group_ambiguous":
		return errors.New("MOFA 中找不到唯一启用的 OpenAI「GPT-不降智」分组")
	case "oauth_exchange_failed":
		return errors.New("MOFA 未能完成 OpenAI 授权换取，请重新发起导入")
	case "import_in_progress":
		return errors.New("MOFA 正在处理同一账号的导入，请稍后重试")
	case "account_changed", "membership_changed":
		return errors.New("MOFA 中的账号或分组在导入期间发生了变化，请重新发起导入")
	case "account_unhealthy":
		return errors.New("MOFA 无法确认现有账号的授权状态，请检查后台错误后重试")
	case "job_not_found":
		return errors.New("MOFA 中的导入请求已失效，请重新发起导入")
	case "admin_session_required", "same_origin_required":
		return errors.New("MOFA 拒绝了本次导入会话，请核对后台地址与管理员设置")
	}
	switch status.status {
	case http.StatusNotFound:
		return errors.New("当前 MOFA 版本未开启 Ant 账号导入接口，请升级 MOFA 后重试")
	case http.StatusUnauthorized:
		return errors.New("MOFA 管理员会话已过期，请重新发起导入")
	case http.StatusGone:
		return errors.New("MOFA 授权请求已过期，请重新发起导入")
	default:
		return fmt.Errorf("MOFA 未完成导入（HTTP %d），请检查后台状态", status.status)
	}
}
