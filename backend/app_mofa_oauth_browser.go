package backend

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

var (
	errMofaOAuthPage     = errors.New("无法打开或连接本实例的 OpenAI 授权页，请重试")
	errMofaOAuthClosed   = errors.New("OpenAI 授权页已关闭或连接中断，请重新发起导入")
	errMofaOAuthCallback = errors.New("OpenAI 授权回调与当前导入不匹配，请重新发起导入")
	errMofaOAuthDenied   = errors.New("OpenAI 未授予授权，请在同一实例重新发起并完成授权")
)

type mofaOAuthCDPEvent struct {
	cdpResponse
	Method string `json:"method"`
	Params struct {
		RequestID    string `json:"requestId"`
		FrameID      string `json:"frameId"`
		ResourceType string `json:"resourceType"`
		URL          string `json:"url"`
		Frame        struct {
			ID       string `json:"id"`
			LoaderID string `json:"loaderId"`
			ParentID string `json:"parentId"`
			URL      string `json:"url"`
		} `json:"frame"`
		Request struct {
			URL    string `json:"url"`
			Method string `json:"method"`
		} `json:"request"`
	} `json:"params"`
}

func (b mofaBrowser) waitMofaOAuth(ctx context.Context, flow mofaStockOAuth, email string, opened func()) (code, state string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	tab, closeTab, err := b.openTab(ctx, false)
	if err != nil {
		return "", "", err
	}
	defer closeTab()
	conn, err := cdpDialWebSocket(tab.Socket)
	if err != nil {
		return "", "", errMofaOAuthPage
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	conn.SetReadLimit(2 << 20)
	deadline, _ := ctx.Deadline()
	_ = conn.SetReadDeadline(deadline)

	var nextID int
	send := func(method string, params map[string]any) (int, error) {
		nextID++
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		return nextID, conn.WriteJSON(cdpMessage{Id: nextID, Method: method, Params: params})
	}
	readResult := func(id int) (map[string]any, error) {
		for {
			var msg mofaOAuthCDPEvent
			if err := conn.ReadJSON(&msg); err != nil {
				return nil, err
			}
			if msg.Id == id {
				if msg.Error != nil {
					return nil, errMofaOAuthPage
				}
				return msg.Result, nil
			}
		}
	}
	frameCommand, err := send("Page.getFrameTree", nil)
	if err != nil {
		return "", "", errMofaOAuthPage
	}
	frames, err := readResult(frameCommand)
	tree, _ := frames["frameTree"].(map[string]any)
	mainFrame, _ := tree["frame"].(map[string]any)
	frameID, _ := mainFrame["id"].(string)
	if err != nil || frameID == "" {
		return "", "", errMofaOAuthPage
	}

	// The native OAuth callback belongs to this tab. Consume its document
	// request before it reaches any other app listening on the fixed port.
	enable, err := send("Fetch.enable", map[string]any{"patterns": []map[string]string{{
		"urlPattern": mofaOAuthRedirectURI + "*", "resourceType": "Document", "requestStage": "Request",
	}}})
	if err != nil {
		return "", "", errMofaOAuthPage
	}
	if _, err := readResult(enable); err != nil {
		return "", "", errMofaOAuthPage
	}
	chooser := mofaOAuthChooser{frameID: frameID, email: mofaStockEmail(email), state: flow.State}
	if chooser.email != "" {
		observe, err := send("Page.enable", nil)
		if err != nil {
			return "", "", errMofaOAuthPage
		}
		_, err = readResult(observe)
		chooser.observing = err == nil
	}
	navigate, err := send("Page.navigate", map[string]any{"url": flow.AuthURL})
	if err != nil {
		return "", "", errMofaOAuthPage
	}
	opened()
	for {
		var msg mofaOAuthCDPEvent
		if err := conn.ReadJSON(&msg); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", "", errors.New("等待 OpenAI 授权超时，请重新发起导入")
			}
			return "", "", errMofaOAuthClosed
		}
		if msg.Id == navigate && (msg.Error != nil || msg.Result["errorText"] != nil || msg.Result["isDownload"] == true) {
			return "", "", errMofaOAuthPage
		}
		if msg.Method != "Fetch.requestPaused" || msg.Params.RequestID == "" {
			if err := chooser.handle(msg, send); err != nil {
				return "", "", errMofaOAuthClosed
			}
			continue
		}
		if msg.Params.FrameID != frameID || msg.Params.ResourceType != "Document" || msg.Params.Request.Method != http.MethodGet {
			if _, err := send("Fetch.failRequest", map[string]any{"requestId": msg.Params.RequestID, "errorReason": "BlockedByClient"}); err != nil {
				return "", "", errMofaOAuthClosed
			}
			continue
		}
		code, state, callbackErr := parseMofaOAuthCallback(msg.Params.Request.URL, flow.State)
		// A 204 leaves the authorization response out of the page's committed
		// navigation history. Wait for Chrome to acknowledge before closing it.
		finish, err := send("Fetch.fulfillRequest", map[string]any{
			"requestId": msg.Params.RequestID, "responseCode": http.StatusNoContent,
			"responseHeaders": []map[string]string{{"name": "Cache-Control", "value": "no-store"}},
		})
		if err != nil {
			return "", "", errMofaOAuthClosed
		}
		if _, err := readResult(finish); err != nil {
			return "", "", errMofaOAuthClosed
		}
		return code, state, callbackErr
	}
}

func parseMofaOAuthCallback(address, expectedState string) (code, state string, err error) {
	if len(address) > 8192 || !strings.HasPrefix(address, mofaOAuthRedirectURI+"?") || strings.Contains(address, "#") {
		return "", "", errMofaOAuthCallback
	}
	values, err := url.ParseQuery(strings.TrimPrefix(address, mofaOAuthRedirectURI+"?"))
	states := values["state"]
	if err != nil || expectedState == "" || len(states) != 1 || subtle.ConstantTimeCompare([]byte(states[0]), []byte(expectedState)) != 1 {
		return "", "", errMofaOAuthCallback
	}
	if values.Has("error") {
		return "", "", errMofaOAuthDenied
	}
	codes := values["code"]
	if len(codes) != 1 || codes[0] == "" || len(codes[0]) > 4096 || strings.ContainsFunc(codes[0], func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) {
		return "", "", errMofaOAuthCallback
	}
	return codes[0], states[0], nil
}
