package backend

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ant-chrome/backend/internal/logger"
)

func TestBrowserStartLogsOmitProxyCredentials(t *testing.T) {
	logger.Init(context.Background(), "info")
	defer logger.Close()
	writer := logger.GetMemoryWriter()
	writer.Clear()
	t.Cleanup(writer.Clear)

	app := newVirtualGroupHealthTestApp(t)
	const saved = "socks5://fixture-user:fixture-saved-secret@127.0.0.1:1080"
	const temporary = "http://temporary-user:fixture-temporary-secret@127.0.0.1:1080"
	node := BrowserProxy{ProxyId: "saved", ProxyName: "saved", ProxyConfig: saved}
	if err := app.SaveBrowserProxies([]BrowserProxy{node}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []browserStartInput{
		{ProfileID: "profile"},
		{ProfileID: "profile", TemporaryProxyConfig: temporary},
	} {
		profile := &BrowserProfile{ProfileId: "profile", ProxyId: node.ProxyId, ProxyConfig: saved}
		_, _, _, _ = app.resolveBrowserStartProxy(input, profile)
	}
	entries := app.GetAppLogs()
	count := 0
	for _, entry := range entries {
		if entry.Message == "代理配置检查" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("proxy decision was logged %d times", count)
	}
	body, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, keyword := range []string{"fixture-user", "temporary-user", "fixture-saved-secret", "fixture-temporary-secret"} {
		if strings.Contains(string(body), keyword) {
			t.Fatal("proxy credentials were written to application logs")
		}
	}
}
