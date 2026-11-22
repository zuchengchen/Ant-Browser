package backend

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type gptSessionCall = func(string, map[string]any) (map[string]any, error)

var chromiumExtensionID = regexp.MustCompile("^[a-p]{32}$")

// Use Chromium's actual unpacked extension path: the runtime ID depends on
// the installation directory and is different from Ant's catalog ID.
func gptExtensionAt(items any, dir string) string {
	extensions, _ := items.([]any)
	for _, item := range extensions {
		ext, _ := item.(map[string]any)
		id, _ := ext["id"].(string)
		path, _ := ext["path"].(string)
		if chromiumExtensionID.MatchString(id) && sameGPTDirectory(path, dir) {
			return id
		}
	}
	return ""
}

func installGPTSession(ctx context.Context, port int, profileDir, extensionDir string, credential *localGPTCredential) error {
	var version cdpBrowserVersion
	if err := fetchBrowserDebugJSONContext(ctx, keyPouchDebugClient, port, "/json/version", &version); err != nil {
		return errGPTCredentialTarget
	}
	// Never follow a remote debugger URL supplied by the process on this port.
	addr := "127.0.0.1:" + strconv.Itoa(port)
	u, err := url.Parse(version.WebSocketDebuggerUrl)
	if err != nil || u.Scheme != "ws" || u.Host != addr || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/devtools/browser/") {
		return errGPTCredentialTarget
	}
	browser, closeBrowser, err := chatGPTCDP(ctx, chatGPTPageTarget{Socket: u.String()})
	if err != nil {
		return errGPTCredentialTarget
	}
	defer closeBrowser()
	tab, err := browser("Target.createTarget", map[string]any{"url": "chrome://version/", "background": true})
	id, _ := tab["targetId"].(string)
	if err != nil || id == "" {
		return errGPTCredentialTarget
	}
	defer func() {
		params := map[string]any{"targetId": id}
		if _, err := browser("Target.closeTarget", params); err == nil {
			return
		}
		// The original context can expire during navigation. Only reconnect
		// to the same browser UUID and close the target this handoff created.
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if call, done, err := chatGPTCDP(cleanup, chatGPTPageTarget{Socket: u.String()}); err == nil {
			defer done()
			_, _ = call("Target.closeTarget", params)
		}
	}()
	page, closePage, err := chatGPTCDP(ctx, chatGPTPageTarget{Socket: "ws://" + addr + "/devtools/page/" + url.PathEscape(id)})
	if err != nil {
		return errGPTCredentialTarget
	}
	defer closePage()
	current, err := waitGPTSessionValue(ctx, page, `location.href === 'chrome://version/' && document.getElementById('profile_path')?.textContent || null`, true)
	path, _ := current["value"].(string)
	if err != nil || !filepath.IsAbs(path) || !sameGPTDirectory(profileDir, filepath.Dir(path)) {
		return errGPTCredentialTarget
	}
	extensions, listErr := browser(cdpMethodExtensionsGet, nil)
	extensionsList := extensions["extensions"]
	if listErr != nil {
		// Older engines load unpacked extensions at launch and expose their
		// verified paths through the built-in extensions manager.
		if err := navigateGPTSession(page, "chrome://extensions/"); err != nil {
			return err
		}
		list, err := waitGPTSessionValue(ctx, page, `(async () => {
			if (location.href !== 'chrome://extensions/' || !chrome.developerPrivate) return null
			const items = await chrome.developerPrivate.getExtensionsInfo({includeDisabled: false})
			return items.map(({id, path}) => ({id, path}))
		})()`, true)
		if err != nil {
			return err
		}
		extensionsList = list["value"]
	}
	if _, ok := extensionsList.([]any); !ok {
		return errGPTCredentialTarget
	}
	extID := gptExtensionAt(extensionsList, extensionDir)
	if extID == "" {
		// Disabling or unloading an extension clears its in-memory storage.
		if credential == nil {
			return nil
		}
		return errGPTCredentialTarget
	}
	handoff := "chrome-extension://" + extID + "/handoff.html"
	if err := navigateGPTSession(page, handoff); err != nil {
		return err
	}
	// Pin the storage object to this exact extension document. A navigation
	// between inspection and delivery invalidates objectId before any write.
	expr := fmt.Sprintf(`location.href === %q && globalThis.chrome?.runtime?.id === %q && chrome.storage?.session || null`, handoff, extID)
	storage, err := waitGPTSessionValue(ctx, page, expr, false)
	objectID, _ := storage["objectId"].(string)
	if err != nil || objectID == "" {
		return errGPTCredentialTarget
	}
	write := `async function () { await this.remove('antLocalGPT'); return true }`
	args := []map[string]any{}
	if credential != nil {
		fields := map[string]any{"site": credential.Site, "recordId": credential.RecordID, "email": credential.Email,
			"password": credential.Password, "totpSecret": credential.TOTPSecret}
		write = `async function (record) {
			const {validateLocalRecord} = await import(chrome.runtime.getURL('local.js'))
			validateLocalRecord(record)
			await this.set({antLocalGPT: record})
			return true
		}`
		args = []map[string]any{{"value": fields}}
	}
	result, err := page("Runtime.callFunctionOn", map[string]any{
		"objectId": objectID, "functionDeclaration": write,
		"arguments": args, "awaitPromise": true, "returnByValue": true,
	})
	ret, _ := result["result"].(map[string]any)
	if err != nil || ret["value"] != true {
		return errors.New("实例的内置扩展未能接收 GPT 登录凭据")
	}
	return nil
}

func navigateGPTSession(page gptSessionCall, address string) error {
	result, err := page("Page.navigate", map[string]any{"url": address})
	if err != nil || result["errorText"] != nil {
		return errGPTCredentialTarget
	}
	return nil
}

func waitGPTSessionValue(ctx context.Context, page gptSessionCall, expression string, byValue bool) (map[string]any, error) {
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		response, err := page("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": byValue, "awaitPromise": true})
		value, _ := response["result"].(map[string]any)
		if err == nil && value != nil && (value["value"] != nil || value["objectId"] != nil) {
			return value, nil
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	return nil, errGPTCredentialTarget
}
