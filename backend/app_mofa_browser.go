package backend

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ant-chrome/backend/internal/browser"
)

const mofaCallbackPath = "/auth/ant-account-import/callback"

var (
	mofaOAuthState = regexp.MustCompile(`^[a-f0-9]{64}$`)
	mofaChallenge  = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	mofaTargetID   = regexp.MustCompile(`^[A-Fa-f0-9]{32}$`)
	errMofaBrowser = errors.New("无法连接所选实例，请确认浏览器窗口仍然打开")
)

type mofaAuthorization struct {
	url      string
	callback string
	state    string
}

func parseMofaAuthorization(raw, site string) (mofaAuthorization, error) {
	invalid := errors.New("MOFA 返回的 OpenAI 授权地址无效，请核对该站点登记的客户端和回调地址")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" || u.User != nil ||
		u.EscapedPath() != "/api/accounts/authorize" || strings.Contains(raw, "#") {
		return mofaAuthorization{}, invalid
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return mofaAuthorization{}, invalid
	}
	fields := []string{"client_id", "redirect_uri", "response_type", "scope", "state", "nonce", "prompt", "code_challenge", "code_challenge_method"}
	if len(q) != len(fields) {
		return mofaAuthorization{}, invalid
	}
	for _, key := range fields {
		if len(q[key]) != 1 || q.Get(key) == "" {
			return mofaAuthorization{}, invalid
		}
	}
	const codexCLIClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	callback, err := url.Parse(q.Get("redirect_uri"))
	state := q.Get("state")
	if err != nil || canonicalMofaCallback(callback) != site+mofaCallbackPath || callback.RawQuery != "" ||
		callback.ForceQuery || strings.Contains(q.Get("redirect_uri"), "#") || q.Get("client_id") == codexCLIClientID ||
		q.Get("response_type") != "code" || q.Get("prompt") != "consent" ||
		q.Get("scope") != "openid profile email offline_access" || q.Get("code_challenge_method") != "S256" ||
		!mofaChallenge.MatchString(q.Get("code_challenge")) || !mofaOAuthState.MatchString(state) ||
		!mofaOAuthState.MatchString(q.Get("nonce")) || state == q.Get("nonce") {
		return mofaAuthorization{}, invalid
	}
	return mofaAuthorization{url: u.String(), callback: site + mofaCallbackPath, state: state}, nil
}

func canonicalMofaCallback(u *url.URL) string {
	if u.Scheme != "https" || u.User != nil || u.EscapedPath() != mofaCallbackPath {
		return ""
	}
	origin, err := normalizeMofaSite("https://" + u.Host)
	if err != nil {
		return ""
	}
	return origin + mofaCallbackPath
}

func (a mofaAuthorization) callbackCode(raw string) (string, bool, error) {
	u, err := url.Parse(raw)
	if err != nil || canonicalMofaCallback(u) != a.callback {
		return "", false, nil
	}
	invalid := errors.New("OpenAI 授权回调与本次导入请求不匹配，请重新发起")
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || strings.Contains(raw, "#") || len(q["state"]) != 1 ||
		subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.state)) != 1 {
		return "", true, invalid
	}
	if len(q["error"]) == 1 && len(q["code"]) == 0 {
		return "", true, errors.New("OpenAI 未确认本次授权，可重新发起导入")
	}
	code := q.Get("code")
	if len(q["error"]) > 0 || len(q["code"]) != 1 || code == "" || len(code) > 2048 ||
		strings.ContainsFunc(code, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", true, invalid
	}
	return code, true, nil
}

type mofaBrowser struct {
	address string
	socket  string
}

func connectMofaBrowser(ctx context.Context, port int) (mofaBrowser, error) {
	var version cdpBrowserVersion
	if err := fetchBrowserDebugJSONContext(ctx, keyPouchDebugClient, port, "/json/version", &version); err != nil {
		return mofaBrowser{}, errMofaBrowser
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	u, err := url.Parse(version.WebSocketDebuggerUrl)
	if err != nil || u.Scheme != "ws" || u.Host != addr || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || !strings.HasPrefix(u.Path, "/devtools/browser/") {
		return mofaBrowser{}, errMofaBrowser
	}
	return mofaBrowser{address: addr, socket: u.String()}, nil
}

func (b mofaBrowser) openTab(ctx context.Context, background bool) (chatGPTPageTarget, func(), error) {
	if err := ctx.Err(); err != nil {
		return chatGPTPageTarget{}, nil, err
	}
	// Finish receiving the target ID if a cancel arrives during creation, so
	// the tab can be closed without losing track of which page we own.
	create, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	call, closeCDP, err := chatGPTCDP(create, chatGPTPageTarget{Socket: b.socket})
	if err != nil {
		return chatGPTPageTarget{}, nil, errMofaBrowser
	}
	defer closeCDP()
	result, err := call("Target.createTarget", map[string]any{"url": "about:blank", "background": background})
	id, _ := result["targetId"].(string)
	if err != nil || !mofaTargetID.MatchString(id) {
		return chatGPTPageTarget{}, nil, errMofaBrowser
	}
	closeTab := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if call, closeCDP, err := chatGPTCDP(cleanup, chatGPTPageTarget{Socket: b.socket}); err == nil {
			defer closeCDP()
			_, _ = call("Target.closeTarget", map[string]any{"targetId": id})
		}
	}
	if err := ctx.Err(); err != nil {
		closeTab()
		return chatGPTPageTarget{}, nil, err
	}
	return chatGPTPageTarget{ID: id, Socket: "ws://" + b.address + "/devtools/page/" + id}, closeTab, nil
}

func (b mofaBrowser) signedInEmail(ctx context.Context, profileDir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	tab, closeTab, err := b.openTab(ctx, true)
	if err != nil {
		return "", err
	}
	defer closeTab()
	page, closeCDP, err := chatGPTCDP(ctx, tab)
	if err != nil {
		return "", errMofaBrowser
	}
	defer closeCDP()
	if err := navigateGPTSession(page, "chrome://version/"); err != nil {
		return "", errMofaBrowser
	}
	value, err := waitGPTSessionValue(ctx, page, `location.href === 'chrome://version/' && document.getElementById('profile_path')?.textContent || null`, true)
	path, _ := value["value"].(string)
	if err != nil || !filepath.IsAbs(path) || !sameGPTDirectory(profileDir, filepath.Dir(path)) {
		return "", errMofaBrowser
	}
	if err := navigateGPTSession(page, "https://chatgpt.com/settings/account"); err != nil {
		return "", errors.New("无法打开本实例的 ChatGPT 账号设置，请检查登录和网络")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := evaluateMofaEmail(page)
		if err == nil {
			switch result["status"] {
			case "verified":
				email, _ := result["email"].(string)
				if len(email) > 0 && len(email) <= 254 {
					return email, nil
				}
			case "ambiguous":
				return "", errors.New("ChatGPT 账号设置显示多个邮箱，请核对本实例的登录账号")
			}
		}
		select {
		case <-ctx.Done():
			return "", errors.New("未能核对本实例的 ChatGPT 邮箱，请确认已登录且能打开账号设置")
		case <-ticker.C:
		}
	}
}

func evaluateMofaEmail(page gptSessionCall) (map[string]any, error) {
	tree, err := page("Page.getFrameTree", nil)
	if err != nil {
		return nil, err
	}
	frameTree, _ := tree["frameTree"].(map[string]any)
	frame, _ := frameTree["frame"].(map[string]any)
	frameID, _ := frame["id"].(string)
	if frameID == "" {
		return nil, errMofaBrowser
	}
	world, err := page("Page.createIsolatedWorld", map[string]any{"frameId": frameID, "worldName": "ant-mofa-account"})
	if err != nil {
		return nil, err
	}
	contextID, _ := world["executionContextId"].(float64)
	if contextID <= 0 {
		return nil, errMofaBrowser
	}
	result, err := page("Runtime.evaluate", map[string]any{
		"contextId": contextID, "returnByValue": true, "expression": browser.MofaAccountEmailExpression(),
	})
	remote, _ := result["result"].(map[string]any)
	value, _ := remote["value"].(map[string]any)
	return value, err
}

func waitMofaAuthorization(ctx context.Context, tab chatGPTPageTarget, authorization mofaAuthorization) (string, error) {
	conn, err := cdpDialWebSocket(tab.Socket)
	if err != nil {
		return "", errMofaBrowser
	}
	defer conn.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	conn.SetReadLimit(2 << 20)
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(cdpMessage{Id: 1, Method: "Page.enable"}); err != nil {
		return "", errMofaBrowser
	}
	for {
		var message struct {
			cdpResponse
			Method string `json:"method"`
			Params struct {
				Frame struct {
					ParentID    string `json:"parentId"`
					URL         string `json:"url"`
					URLFragment string `json:"urlFragment"`
				} `json:"frame"`
			} `json:"params"`
		}
		if err := conn.ReadJSON(&message); err != nil {
			return "", errors.New("OpenAI 授权标签页已关闭或连接中断，请重新发起导入")
		}
		if message.Id == 1 {
			if message.Error != nil || conn.WriteJSON(cdpMessage{Id: 2, Method: "Page.navigate", Params: map[string]any{"url": authorization.url}}) != nil {
				return "", errMofaBrowser
			}
		}
		if message.Id == 2 && (message.Error != nil || message.Result["errorText"] != nil) {
			return "", errors.New("无法打开 OpenAI 授权页，请检查该实例的网络连接")
		}
		if message.Method == "Page.frameNavigated" && message.Params.Frame.ParentID == "" {
			frame := message.Params.Frame
			if code, matched, err := authorization.callbackCode(frame.URL + frame.URLFragment); matched {
				return code, err
			}
		}
	}
}
