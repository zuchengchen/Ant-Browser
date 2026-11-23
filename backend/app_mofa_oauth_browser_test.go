package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMofaOAuthCallback(t *testing.T) {
	for _, tc := range []struct {
		name, address string
		want          error
	}{
		{"verified", mofaOAuthRedirectURI + "?code=fixture%2Bcode&state=fixture-state", nil},
		{"wrong host", "http://127.0.0.1:1455/auth/callback?code=x&state=fixture-state", errMofaOAuthCallback},
		{"wrong path", mofaOAuthRedirectURI + "/?code=x&state=fixture-state", errMofaOAuthCallback},
		{"wrong state", mofaOAuthRedirectURI + "?code=x&state=other", errMofaOAuthCallback},
		{"missing state", mofaOAuthRedirectURI + "?code=x", errMofaOAuthCallback},
		{"duplicate state", mofaOAuthRedirectURI + "?code=x&state=fixture-state&state=other", errMofaOAuthCallback},
		{"duplicate code", mofaOAuthRedirectURI + "?code=x&code=y&state=fixture-state", errMofaOAuthCallback},
		{"malformed code", mofaOAuthRedirectURI + "?code=%zz&state=fixture-state", errMofaOAuthCallback},
		{"fragment", mofaOAuthRedirectURI + "?code=x&state=fixture-state#fragment", errMofaOAuthCallback},
		{"denied", mofaOAuthRedirectURI + "?error=access_denied&state=fixture-state", errMofaOAuthDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, state, err := parseMofaOAuthCallback(tc.address, "fixture-state")
			if !errors.Is(err, tc.want) {
				t.Fatalf("callback error = %v, want %v", err, tc.want)
			}
			if err == nil && (code != "fixture+code" || state != "fixture-state") {
				t.Fatal("verified callback did not preserve the code and state")
			}
			if err != nil && (code != "" || state != "") {
				t.Fatal("unverified callback returned OAuth data")
			}
		})
	}
}

func TestMofaOAuthBrowserUsesOnlyItsMainFrame(t *testing.T) {
	const targetID = "11223344556677889900112233445566"
	const chooserURL = "https://auth.openai.com/choose-an-account"
	const email = "chat+test@example.invalid"
	flow := mofaStockOAuth{AuthURL: "https://auth.openai.com/oauth/authorize?state=fixture-state", State: "fixture-state"}
	closed := make(chan struct{}, 1)
	chooserCalled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		respond := func(call cdpMessage, result map[string]any) {
			if err := conn.WriteJSON(cdpResponse{Id: call.Id, Result: result}); err != nil {
				t.Error(err)
			}
		}
		pause := func(id, frameID, code string) {
			err := conn.WriteJSON(map[string]any{"method": "Fetch.requestPaused", "params": map[string]any{
				"requestId": id, "frameId": frameID, "resourceType": "Document", "request": map[string]any{
					"url": mofaOAuthRedirectURI + "?code=" + code + "&state=fixture-state", "method": "GET",
				},
			}})
			if err != nil {
				t.Error(err)
			}
		}
		navigateFrame := func(id, parent, address string) {
			if err := conn.WriteJSON(map[string]any{"method": "Page.frameNavigated", "params": map[string]any{
				"frame": map[string]any{"id": id, "parentId": parent, "url": address},
			}}); err != nil {
				t.Error(err)
			}
		}
		for {
			var call cdpMessage
			if conn.ReadJSON(&call) != nil {
				return
			}
			switch call.Method {
			case "Target.createTarget":
				respond(call, map[string]any{"targetId": targetID})
			case "Target.closeTarget":
				if call.Params["targetId"] != targetID {
					t.Error("attempted to close another target")
				}
				closed <- struct{}{}
				respond(call, nil)
			case "Page.getFrameTree":
				if r.URL.Path != "/devtools/page/"+targetID {
					t.Error("authorization was not attached to the new tab")
				}
				respond(call, map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "root"}}})
			case "Fetch.enable":
				patterns, _ := call.Params["patterns"].([]any)
				if len(patterns) != 1 || patterns[0].(map[string]any)["urlPattern"] != mofaOAuthRedirectURI+"*" {
					t.Error("callback interceptor is not scoped to the loopback redirect")
				}
				respond(call, nil)
			case "Page.enable":
				respond(call, nil)
			case "Page.navigate":
				if call.Params["url"] != flow.AuthURL {
					t.Error("navigation did not use the issued authorization URL")
				}
				respond(call, nil)
				navigateFrame("root", "", "about:blank")
				navigateFrame("subframe", "root", chooserURL)
				navigateFrame("root", "", flow.AuthURL)
				navigateFrame("root", "", chooserURL)
				navigateFrame("root", "", chooserURL)
			case "Page.createIsolatedWorld":
				if call.Params["frameId"] != "root" {
					t.Error("an isolated world was created outside the main frame")
				}
				respond(call, map[string]any{"executionContextId": 34})
			case "Runtime.evaluate":
				if call.Params["contextId"] != float64(34) ||
					!strings.HasSuffix(call.Params["expression"].(string), `(["`+chooserURL+`","`+email+`"])`) {
					t.Error("chooser did not use the verified email in the isolated world")
				}
				select {
				case chooserCalled <- struct{}{}:
				default:
					t.Error("chooser was evaluated more than once")
				}
				// The redirect can reach Fetch before Runtime.evaluate returns.
				pause("other", "subframe", "wrong-code")
			case "Fetch.failRequest":
				if call.Params["requestId"] != "other" {
					t.Error("wrong request blocked")
				}
				respond(call, nil)
				pause("the-callback", "root", "correct-code")
			case "Fetch.fulfillRequest":
				if call.Params["requestId"] != "the-callback" || call.Params["responseCode"] != float64(http.StatusNoContent) {
					t.Error("callback was not consumed locally")
				}
				respond(call, nil)
			default:
				t.Errorf("unexpected CDP method %s", call.Method)
				return
			}
		}
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	browser := mofaBrowser{address: addr, socket: "ws://" + addr + "/devtools/browser/test"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened := false
	code, state, err := browser.waitMofaOAuth(ctx, flow, email, func() { opened = true })
	if err != nil || code != "correct-code" || state != flow.State || !opened {
		t.Fatalf("authorization callback was not verified: opened=%t, err=%v", opened, err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("owned tab was not closed")
	}
	select {
	case <-chooserCalled:
	default:
		t.Fatal("the chooser was not evaluated")
	}
}

func TestMofaOAuthChooserNavigationGuards(t *testing.T) {
	const chooserURL = "https://auth.openai.com/choose-an-account"
	for _, tc := range []struct {
		name      string
		addresses []string
		want      bool
	}{
		{"exact path", []string{chooserURL}, true},
		{"matching state", []string{chooserURL + "?state=fixture-state"}, true},
		{"unexpected state", []string{chooserURL + "?state=other"}, false},
		{"duplicate state", []string{chooserURL + "?state=fixture-state&state=other"}, false},
		{"consent", []string{"https://auth.openai.com/consent"}, false},
		{"partial path", []string{chooserURL + "/other"}, false},
		{"foreign origin before chooser", []string{"https://example.invalid/choose-an-account", chooserURL}, false},
		{"unsecured origin before chooser", []string{"http://auth.openai.com/choose-an-account", chooserURL}, false},
		{"auth tab navigated away", []string{"https://auth.openai.com/oauth/authorize", "about:blank", chooserURL}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := mofaOAuthChooser{frameID: "root", state: "fixture-state", observing: true}
			called := false
			for _, address := range tc.addresses {
				msg := mofaOAuthCDPEvent{Method: "Page.navigatedWithinDocument"}
				msg.Params.FrameID, msg.Params.URL = "root", address
				if err := c.handle(msg, func(method string, _ map[string]any) (int, error) {
					called = true
					return 1, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if called != tc.want {
				t.Fatalf("chooser evaluated = %t, want %t", called, tc.want)
			}
		})
	}
}

func TestMofaOAuthChooserDiscardsWorldAfterReload(t *testing.T) {
	chooser := mofaOAuthChooser{frameID: "root", state: "fixture-state", observing: true}
	sent := []string{}
	send := func(method string, _ map[string]any) (int, error) {
		sent = append(sent, method)
		return len(sent), nil
	}
	msg := mofaOAuthCDPEvent{Method: "Page.frameNavigated"}
	msg.Params.Frame.ID = "root"
	msg.Params.Frame.URL = "https://auth.openai.com/choose-an-account"
	msg.Params.Frame.LoaderID = "first-document"
	if err := chooser.handle(msg, send); err != nil {
		t.Fatal(err)
	}
	msg.Params.Frame.LoaderID = "reloaded-document"
	if err := chooser.handle(msg, send); err != nil {
		t.Fatal(err)
	}
	response := mofaOAuthCDPEvent{cdpResponse: cdpResponse{Id: 1, Result: map[string]any{"executionContextId": float64(9)}}}
	if err := chooser.handle(response, send); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0] != "Page.createIsolatedWorld" {
		t.Fatalf("stale observation ran after reloading: %v", sent)
	}
}
