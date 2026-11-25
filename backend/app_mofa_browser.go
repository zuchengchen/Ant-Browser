package backend

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ant-chrome/backend/internal/browser"
)

var (
	mofaTargetID                = regexp.MustCompile(`^[A-Fa-f0-9]{32}$`)
	errMofaBrowser              = errors.New("无法连接所选实例，请确认浏览器窗口仍然打开")
	errMofaBrowserLoginRequired = errors.New("请在所选浏览器实例中登录 ChatGPT 后重新导入")
)

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

func (b mofaBrowser) openBackgroundTab(ctx context.Context) (chatGPTPageTarget, func(), error) {
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
	result, err := call("Target.createTarget", map[string]any{"url": "about:blank", "background": true})
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

func (b mofaBrowser) activateOAuthTab(ctx context.Context, targetID string) error {
	call, closeCDP, err := chatGPTCDP(ctx, chatGPTPageTarget{Socket: b.socket})
	if err != nil {
		return errMofaOAuthClosed
	}
	defer closeCDP()
	params := map[string]any{"targetId": targetID}
	result, err := call("Target.getTargetInfo", params)
	info, _ := result["targetInfo"].(map[string]any)
	if err != nil || info["targetId"] != targetID || info["type"] != "page" || ctx.Err() != nil {
		return errMofaOAuthClosed
	}
	if _, err := call("Target.activateTarget", params); err != nil {
		return errMofaOAuthClosed
	}
	return nil
}

func (b mofaBrowser) signedInEmail(ctx context.Context, profileDir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	tab, closeTab, err := b.openBackgroundTab(ctx)
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
		if errors.Is(err, errMofaBrowserLoginRequired) {
			return "", err
		}
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
	address, _ := frame["url"].(string)
	if mofaAuthPagePhase(address) == mofaOAuthLoginRequiredPhase {
		return nil, errMofaBrowserLoginRequired
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
