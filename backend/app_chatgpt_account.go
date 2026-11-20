package backend

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed chatgpt_settings_parser.js
var chatGPTSettingsParser string

//go:embed chatgpt_account_reader.js
var chatGPTAccountReaderFactory string

var chatGPTAccountReader = "(" + chatGPTAccountReaderFactory + ")(" + chatGPTSettingsParser + ")"

type ChatGPTUsageWindow struct {
	RemainingPercent *float64 `json:"remainingPercent"`
	ResetText        string   `json:"resetText"`
}

type ChatGPTAccountInfo struct {
	ProfileID          string             `json:"profileId"`
	Status             string             `json:"status"`
	CheckedAt          string             `json:"checkedAt"`
	SavedAt            string             `json:"savedAt"`
	DisplayName        string             `json:"displayName"`
	Username           string             `json:"username"`
	Email              string             `json:"email"`
	Plan               string             `json:"plan"`
	RenewalDate        string             `json:"renewalDate"`
	ExpiryDate         string             `json:"expiryDate"`
	SubscriptionDetail string             `json:"subscriptionDetail"`
	Weekly             ChatGPTUsageWindow `json:"weekly"`
	FiveHour           ChatGPTUsageWindow `json:"fiveHour"`
	Credits            *float64           `json:"credits"`
	ResetCards         *int               `json:"resetCards"`
	UsageScope         string             `json:"usageScope"`
	Warnings           []string           `json:"warnings"`
}

type chatGPTInspectionSession struct {
	mu       sync.Mutex
	targetID string
}

type chatGPTPageTarget struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	URL    string `json:"url"`
	Socket string `json:"webSocketDebuggerUrl"`
}

func isChatGPTInspectionURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" && (u.Hostname() == "chatgpt.com" || u.Hostname() == "auth.openai.com")
}

var chatGPTProfileEmail = regexp.MustCompile(`(?i)(?:^|\s)([^\s<>@]+@[^\s<>@]+\.[a-z]{2,})(?:$|\s)`)

func expectedChatGPTEmail(name string) string {
	match := chatGPTProfileEmail.FindStringSubmatch(strings.TrimSpace(name))
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

// BrowserChatGPTAccountRead starts the selected profile through the normal proxy
// stack and reads visible ChatGPT settings. Verified results are saved locally;
// login, verification, mismatch and failed reads preserve the previous snapshot.
func (a *App) BrowserChatGPTAccountRead(profileID string) (ChatGPTAccountInfo, error) {
	profileID = strings.TrimSpace(profileID)
	if a == nil || a.browserMgr == nil || profileID == "" {
		return ChatGPTAccountInfo{}, fmt.Errorf("请先选择有效实例")
	}
	if a.db == nil {
		return ChatGPTAccountInfo{}, fmt.Errorf("账户信息数据库尚未就绪，请稍后重试")
	}
	a.chatGPTMu.Lock()
	if a.chatGPTSessions == nil {
		a.chatGPTSessions = make(map[string]*chatGPTInspectionSession)
	}
	session := a.chatGPTSessions[profileID]
	if session == nil {
		session = &chatGPTInspectionSession{}
		a.chatGPTSessions[profileID] = session
	}
	a.chatGPTMu.Unlock()
	if !session.mu.TryLock() {
		return ChatGPTAccountInfo{}, fmt.Errorf("该实例正在读取 ChatGPT 账户，请稍候")
	}
	defer session.mu.Unlock()
	a.runtimeMu.RLock()
	ctx := a.ctx
	a.runtimeMu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()

	profile, err := a.BrowserInstanceStartWithParams(profileID, nil, nil, true)
	if err != nil {
		return ChatGPTAccountInfo{}, err
	}
	port, err := a.getDebugPort(profileID)
	if err != nil {
		return ChatGPTAccountInfo{}, err
	}
	target, err := prepareChatGPTInspectionTarget(port, session.targetID)
	if err != nil {
		return ChatGPTAccountInfo{}, err
	}
	session.targetID = target.ID
	expectedEmail := expectedChatGPTEmail(profile.ProfileName)
	result, err := readChatGPTAccountPage(ctx, target, expectedEmail)
	result.ProfileID, result.CheckedAt = profileID, time.Now().Format(time.RFC3339)
	if err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, fmt.Errorf("GPT 操作已停止或超时，请重新获取")
	}
	if err := a.saveChatGPTAccount(&result); err != nil {
		return result, fmt.Errorf("账户信息已读取，但保存失败：%w", err)
	}
	return result, nil
}

func prepareChatGPTInspectionTarget(port int, previousID string) (chatGPTPageTarget, error) {
	list := func() ([]chatGPTPageTarget, error) {
		body, err := cdpGetEndpointBody(port, "/json/list")
		if err != nil {
			return nil, err
		}
		var targets []chatGPTPageTarget
		err = json.Unmarshal(body, &targets)
		return targets, err
	}
	targets, err := list()
	if err != nil {
		return chatGPTPageTarget{}, err
	}
	for _, target := range targets {
		if target.ID == previousID && target.Type == "page" {
			return target, nil
		}
	}
	// Create a dedicated tab: never navigate a user's conversation or switch accounts.
	created, err := cdpBrowserCallResult(port, "Target.createTarget", map[string]any{"url": "https://chatgpt.com/settings/account"})
	if err != nil {
		return chatGPTPageTarget{}, err
	}
	id, _ := created["targetId"].(string)
	for i := 0; i < 20; i++ {
		targets, err = list()
		if err != nil {
			return chatGPTPageTarget{}, err
		}
		for _, target := range targets {
			if target.ID == id && target.Socket != "" {
				return target, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return chatGPTPageTarget{}, fmt.Errorf("ChatGPT 查询页面尚未就绪，请重试")
}

func readChatGPTAccountPage(ctx context.Context, target chatGPTPageTarget, email string) (ChatGPTAccountInfo, error) {
	call, closeCDP, err := chatGPTCDP(ctx, target)
	if err != nil {
		return ChatGPTAccountInfo{}, err
	}
	defer closeCDP()
	// Do not interrupt a login/verification flow on subsequent polls.
	current, err := call("Runtime.evaluate", map[string]any{"expression": "({url: location.href, waiting: location.hostname === 'auth.openai.com' || /\\/(?:auth|login)(?:\\/|$)/.test(location.pathname) || /just a moment|security verification/i.test(document.title)})", "returnByValue": true})
	if err != nil {
		return ChatGPTAccountInfo{}, err
	}
	remote, _ := current["result"].(map[string]any)
	value, _ := remote["value"].(map[string]any)
	currentURL, _ := value["url"].(string)
	waiting, _ := value["waiting"].(bool)
	if u, parseErr := url.Parse(currentURL); parseErr == nil && u.Scheme == "https" && (u.Hostname() == "accounts.google.com" || u.Hostname() == "appleid.apple.com" || u.Hostname() == "login.live.com" || u.Hostname() == "login.microsoftonline.com") {
		return ChatGPTAccountInfo{Status: "login_required", Warnings: []string{"请在实例浏览器中完成第三方账号登录，随后自动继续。"}}, nil
	}
	if currentURL != "about:blank" && !isChatGPTInspectionURL(currentURL) {
		return ChatGPTAccountInfo{}, fmt.Errorf("查询标签页已离开 ChatGPT，请完成登录后重试")
	}
	if !waiting && currentURL != "https://chatgpt.com/settings/account" {
		if _, err = call("Page.navigate", map[string]any{"url": "https://chatgpt.com/settings/account"}); err != nil {
			return ChatGPTAccountInfo{}, err
		}
	}
	// Navigation may replace the JS context; retry only context destruction, without
	// repeating navigation or actions on the login form.
	options, _ := json.Marshal(map[string]any{"expectedEmail": email})
	expression := "(" + chatGPTAccountReader + ").read(" + string(options) + ")"
	var response map[string]any
	for attempt := 0; attempt < 4; attempt++ {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ChatGPTAccountInfo{}, ctx.Err()
		case <-timer.C:
		}
		response, err = call("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true, "timeout": 65000})
		if err == nil || !strings.Contains(err.Error(), "context") {
			break
		}
	}
	if err != nil {
		return ChatGPTAccountInfo{}, fmt.Errorf("读取 ChatGPT 账户失败：%w", err)
	}
	remote, _ = response["result"].(map[string]any)
	data, err := json.Marshal(remote["value"])
	if err != nil {
		return ChatGPTAccountInfo{}, err
	}
	var info ChatGPTAccountInfo
	if err = json.Unmarshal(data, &info); err != nil || info.Status == "" {
		return ChatGPTAccountInfo{}, fmt.Errorf("ChatGPT 页面未返回有效账户信息，请重试")
	}
	return info, nil
}
