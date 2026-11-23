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

type mofaHTTPError struct {
	status int
}

func (e *mofaHTTPError) Error() string { return fmt.Sprintf("MOFA 返回 HTTP %d", e.status) }

func (c *mofaClient) request(ctx context.Context, method, path string, input, output any) error {
	return c.requestWithHeaders(ctx, method, path, input, output, nil)
}

func (c *mofaClient) requestWithHeaders(ctx context.Context, method, path string, input, output any, headers http.Header) error {
	var payload io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return errors.New("无法创建 MOFA 请求")
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.site+"/api/v1"+path, payload)
	if err != nil {
		return errors.New("MOFA 后台地址无效")
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
	if resp.StatusCode != http.StatusOK {
		return &mofaHTTPError{status: resp.StatusCode}
	}
	const maxBody = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if ctx.Err() != nil {
		return ctx.Err()
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
		if err := c.request(requestCtx, http.MethodPost, path, fields, &response); err != nil {
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
