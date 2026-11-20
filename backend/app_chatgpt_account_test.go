package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestChatGPTAccountURLAndExpectedEmail(t *testing.T) {
	for raw, valid := range map[string]bool{
		"https://chatgpt.com/settings/account": true,
		"https://auth.openai.com/log-in":       true,
		"http://chatgpt.com/settings/account":  false,
		"https://chatgpt.com.example.org/":     false,
		"https://chatgpt.com@evil.example/":    false,
		"https://chatgpt.com:444/":             false,
	} {
		if isChatGPTInspectionURL(raw) != valid {
			t.Errorf("unexpected URL classification: %s", raw)
		}
	}
	if got := expectedChatGPTEmail("example+test@gmail.com"); got != "example+test@gmail.com" {
		t.Fatal(got)
	}
	if got := expectedChatGPTEmail("ordinary profile"); got != "" {
		t.Fatal(got)
	}
}

func TestChatGPTReadSkipsEventsAndKeepsMissingValues(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var req cdpMessage
			if conn.ReadJSON(&req) != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{"method": "Runtime.executionContextCreated"})
			value := map[string]any{"url": "https://chatgpt.com/settings/account", "waiting": false}
			if strings.Contains(req.Params["expression"].(string), ".read(") {
				value = map[string]any{"status": "partial", "credits": nil, "resetCards": 0, "weekly": map[string]any{"remainingPercent": 0}, "warnings": []string{"Missing credits"}}
			}
			_ = conn.WriteJSON(map[string]any{"id": req.Id, "result": map[string]any{"result": map[string]any{"value": value}}})
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := readChatGPTAccountPage(ctx, chatGPTPageTarget{Socket: "ws" + strings.TrimPrefix(server.URL, "http")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Credits != nil || got.ResetCards == nil || *got.ResetCards != 0 || got.Weekly.RemainingPercent == nil || *got.Weekly.RemainingPercent != 0 {
		t.Fatalf("lost null/zero distinction: %+v", got)
	}
}

func TestChatGPTLiveAccountRead(t *testing.T) {
	port, _ := strconv.Atoi(os.Getenv("ANT_CHATGPT_TEST_PORT"))
	if port == 0 {
		t.Skip("explicit authenticated browser required")
	}
	target, err := prepareChatGPTInspectionTarget(port, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cdpBrowserCall(port, "Target.closeTarget", map[string]any{"targetId": target.ID}) }()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	result, err := readChatGPTAccountPage(ctx, target, os.Getenv("ANT_CHATGPT_TEST_EMAIL"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" {
		t.Fatalf("live check status = %s, warnings = %v", result.Status, result.Warnings)
	}
	if result.Email == "" || result.DisplayName == "" || result.Plan == "" || result.Weekly.RemainingPercent == nil || result.Credits == nil || result.ResetCards == nil {
		t.Fatal("required account fields missing")
	}
	if path := os.Getenv("ANT_CHATGPT_TEST_OUTPUT"); path != "" {
		result.CheckedAt = time.Now().Format(time.RFC3339)
		data, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
