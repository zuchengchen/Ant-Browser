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
	flow := mofaStockOAuth{AuthURL: "https://auth.openai.com/oauth/authorize?state=fixture-state", State: "fixture-state"}
	closed := make(chan struct{}, 1)
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
			case "Page.navigate":
				if call.Params["url"] != flow.AuthURL {
					t.Error("navigation did not use the issued authorization URL")
				}
				respond(call, nil)
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
	code, state, err := browser.waitMofaOAuth(ctx, flow, func() { opened = true })
	if err != nil || code != "correct-code" || state != flow.State || !opened {
		t.Fatalf("authorization callback was not verified: opened=%t, err=%v", opened, err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("owned tab was not closed")
	}
}
