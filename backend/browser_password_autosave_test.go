package backend

import (
	"context"
	"fmt"
	"testing"

	"ant-chrome/backend/internal/browser"
)

type passwordAXTestNode struct {
	role     uint32
	name     string
	children []browserPasswordAccessible
	states   []uint32
	actions  []string
}
type passwordAXTestReader struct {
	t     *testing.T
	nodes map[browserPasswordAccessible]*passwordAXTestNode
}

func (r *passwordAXTestReader) Role(ref browserPasswordAccessible) (uint32, error) {
	if n := r.nodes[ref]; n != nil {
		return n.role, nil
	}
	return 0, fmt.Errorf("missing")
}
func (r *passwordAXTestReader) Name(ref browserPasswordAccessible) (string, error) {
	n := r.nodes[ref]
	if n.role != 2 && n.role != 16 && n.role != 43 {
		r.t.Fatalf("read name from non-dialog/button role %d", n.role)
	}
	return n.name, nil
}
func (r *passwordAXTestReader) State(ref browserPasswordAccessible) ([]uint32, error) {
	return r.nodes[ref].states, nil
}
func (r *passwordAXTestReader) Actions(ref browserPasswordAccessible) ([]string, error) {
	if r.nodes[ref].role != 43 {
		r.t.Fatal("queried non-button actions")
	}
	return r.nodes[ref].actions, nil
}
func (r *passwordAXTestReader) Children(ref browserPasswordAccessible) ([]browserPasswordAccessible, error) {
	if browserPasswordAXPruned(r.nodes[ref].role) {
		r.t.Fatalf("entered text/password/document subtree %d", r.nodes[ref].role)
	}
	return r.nodes[ref].children, nil
}

func TestBrowserPasswordAccessibleRejectsWebAndAmbiguousDialogs(t *testing.T) {
	for _, variant := range []string{"save", "update", "cross_origin", "duplicate_reference", "web_fake", "embedded_fake", "missing_password", "wrong_title", "never", "disabled", "ambiguous"} {
		t.Run(variant, func(t *testing.T) {
			ref := func(path string) browserPasswordAccessible {
				return browserPasswordAccessible{Bus: ":1.22", Path: path}
			}
			root, dialog, password, button, other := ref("/root"), ref("/dialog"), ref("/password"), ref("/button"), ref("/other")
			states := []uint32{1<<8 | 1<<24 | 1<<25 | 1<<30}
			r := &passwordAXTestReader{t: t, nodes: map[browserPasswordAccessible]*passwordAXTestNode{root: {role: 75, children: []browserPasswordAccessible{dialog}}, dialog: {role: 2, name: "Save password?", states: states, children: []browserPasswordAccessible{password, button}}, password: {role: 40}, button: {role: 43, name: "Save", states: states, actions: []string{"press", "showContextMenu"}}}}
			switch variant {
			case "update":
				r.nodes[dialog].name = "要更新密码吗？"
				r.nodes[button].name = "更新密码"
			case "cross_origin":
				r.nodes[dialog].name = "Save password for auth.example.test?"
			case "duplicate_reference":
				r.nodes[root].children = append(r.nodes[root].children, dialog)
			case "web_fake":
				r.nodes[other] = &passwordAXTestNode{role: 95, children: []browserPasswordAccessible{dialog}}
				r.nodes[root].children = []browserPasswordAccessible{other}
			case "embedded_fake":
				r.nodes[other] = &passwordAXTestNode{role: 78, children: []browserPasswordAccessible{dialog}}
				r.nodes[root].children = []browserPasswordAccessible{other}
			case "missing_password":
				r.nodes[dialog].children = []browserPasswordAccessible{button}
			case "wrong_title":
				r.nodes[dialog].name = "Delete saved password?"
			case "never":
				r.nodes[button].name = "Never"
			case "disabled":
				r.nodes[button].states = []uint32{1<<25 | 1<<30}
			case "ambiguous":
				r.nodes[other] = &passwordAXTestNode{role: 43, name: "Save", states: states, actions: []string{"press"}}
				r.nodes[dialog].children = append(r.nodes[dialog].children, other)
			}
			buttons, err := inspectBrowserPasswordAccessible(r, root)
			if err != nil {
				t.Fatal(err)
			}
			want := variant == "save" || variant == "update" || variant == "cross_origin" || variant == "duplicate_reference"
			if (len(buttons) == 1) != want {
				t.Fatalf("buttons=%#v", buttons)
			}
			if want && (buttons[0].Ref != button || buttons[0].Dialog != dialog || buttons[0].Action != 0) {
				t.Fatalf("wrong native binding: %#v", buttons)
			}
		})
	}
}

func TestBrowserPasswordNativeOriginAndTitles(t *testing.T) {
	for _, title := range []string{"Save password?", "Update password?", "要儲存密碼嗎？", "要保存 auth.example.test 的密码吗？", "Update password for [::1]:8080?"} {
		if len(browserPasswordDialogLabels(title)) == 0 {
			t.Errorf("valid native title rejected: %s", title)
		}
	}
	for _, title := range []string{"Save password for ?", "Save password for example.test/path?", "Save password for user@example.test?", "Save password for evil\n.example?", "Save password for evil\u00a0.example?", "Delete password?", "Save my password?"} {
		if len(browserPasswordDialogLabels(title)) != 0 {
			t.Errorf("unsafe title accepted: %s", title)
		}
	}
}

func TestBrowserPasswordAutosaveRejectsCancelledRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{browserMgr: &browser.Manager{}}
	app.setRuntimeContext(ctx)
	cancel()
	app.ensureBrowserPasswordAutosave()
	app.browserPasswordAutosaveMu.Lock()
	worker := app.browserPasswordAutosaveWorker
	app.browserPasswordAutosaveMu.Unlock()
	if worker != nil {
		t.Fatal("started watcher under cancelled runtime")
	}
	app.stopRuntimeEvents()
	app.waitBackgroundTasks()
}

func TestBrowserPasswordAutosaveMaintenanceRestartsSingleWorker(t *testing.T) {
	if !browserPasswordAutosaveSupported() {
		t.Skip("platform does not run this watcher")
	}
	app := &App{browserMgr: &browser.Manager{}}
	app.setRuntimeContext(context.Background())
	defer func() { app.stopRuntimeEvents(); app.waitBackgroundTasks() }()
	app.ensureBrowserPasswordAutosave()
	app.browserPasswordAutosaveMu.Lock()
	first := app.browserPasswordAutosaveWorker
	app.browserPasswordAutosaveMu.Unlock()
	if first == nil {
		t.Fatal("worker did not start")
	}
	app.ensureBrowserPasswordAutosave()
	app.browserPasswordAutosaveMu.Lock()
	second := app.browserPasswordAutosaveWorker
	app.browserPasswordAutosaveMu.Unlock()
	if first != second {
		t.Fatal("started competing worker")
	}
	app.cancelBackgroundTasksAndWait()
	app.browserPasswordAutosaveMu.Lock()
	stopped := app.browserPasswordAutosaveWorker
	app.browserPasswordAutosaveMu.Unlock()
	if stopped != nil {
		t.Fatal("cancelled worker did not clean up")
	}
	app.resumeBackgroundTasks()
	app.browserPasswordAutosaveMu.Lock()
	resumed := app.browserPasswordAutosaveWorker
	app.browserPasswordAutosaveMu.Unlock()
	if resumed == nil || resumed == first {
		t.Fatal("maintenance did not resume a fresh worker")
	}
}
