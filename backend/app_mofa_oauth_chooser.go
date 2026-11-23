package backend

import (
	_ "embed"
	"encoding/json"
	"net/url"
)

//go:embed mofa_oauth_account_chooser.js
var mofaOAuthAccountChooserScript string

type mofaOAuthChooser struct {
	frameID       string
	email         string
	state         string
	observing     bool
	committedAuth bool
	world         int
	address       string
	loaderID      string
}

func (c *mofaOAuthChooser) handle(msg mofaOAuthCDPEvent, send func(string, map[string]any) (int, error)) error {
	if c.world != 0 && msg.Id == c.world {
		c.world = 0
		contextID, _ := msg.Result["executionContextId"].(float64)
		if msg.Error != nil || contextID <= 0 {
			return nil
		}
		arguments, err := json.Marshal([]string{c.address, c.email})
		if err != nil {
			return err
		}
		// The callback can arrive before the script returns. Read it in the
		// OAuth event loop instead of waiting for this command's response.
		_, err = send("Runtime.evaluate", map[string]any{
			"contextId": contextID, "expression": "(" + mofaOAuthAccountChooserScript + ")(" + string(arguments) + ")",
			"awaitPromise": true, "returnByValue": true,
		})
		return err
	}
	if !c.observing && c.world == 0 {
		return nil
	}
	var address string
	switch msg.Method {
	case "Page.frameNavigated":
		if msg.Params.Frame.ID != c.frameID || msg.Params.Frame.ParentID != "" {
			return nil
		}
		address = msg.Params.Frame.URL
	case "Page.navigatedWithinDocument":
		if msg.Params.FrameID != c.frameID {
			return nil
		}
		address = msg.Params.URL
	default:
		return nil
	}
	if c.world != 0 {
		if msg.Method == "Page.frameNavigated" && address == c.address && msg.Params.Frame.LoaderID == c.loaderID {
			return nil
		}
		c.world = 0
		return nil
	}
	if address == "about:blank" && !c.committedAuth {
		return nil
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" || u.User != nil {
		c.observing = false
		return nil
	}
	c.committedAuth = true
	if u.EscapedPath() != "/choose-an-account" {
		return nil
	}
	// Only try once in this OAuth tab, even if OpenAI displays the chooser again.
	c.observing = false
	query, err := url.ParseQuery(u.RawQuery)
	states := query["state"]
	if err != nil || (len(states) != 0 && (len(states) != 1 || states[0] != c.state)) {
		return nil
	}
	c.address = address
	c.loaderID = msg.Params.Frame.LoaderID
	c.world, err = send("Page.createIsolatedWorld", map[string]any{
		"frameId": c.frameID, "worldName": "ant-mofa-oauth-chooser",
	})
	return err
}
