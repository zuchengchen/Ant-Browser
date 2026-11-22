package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/keypouch"
	"github.com/google/uuid"
)

type keyPouchOpenClaim struct {
	Action         string `json:"action"`
	RecordID       int64  `json:"record_id"`
	Email          string `json:"email"`
	GroupID        int64  `json:"group_id"`
	GroupName      string `json:"group_name"`
	CredentialSync bool   `json:"credential_sync"`
}

const keyPouchTemplate = "KeyPouch 实例模板"

func keyPouchProfileID(site string, recordID int64) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(site+"/records/"+strconv.FormatInt(recordID, 10))).String()
}

// Only a user-confirmed local ID may be adopted. The site's grammar also
// permits IDs from older Ant releases, while UUIDv5 identifies bound copies.
var keyPouchLocalID = regexp.MustCompile("^[A-Za-z0-9_-]{1,128}$")

type keyPouchProfileMatch struct {
	bound, candidate, template string
}

func (a *App) keyPouchProfile(site string, c keyPouchOpenClaim, links *keypouch.ProfileLinks, acceptExisting func() bool) (string, string) {
	m := a.browserMgr
	if m == nil || m.ProfileDAO == nil || m.GroupDAO == nil {
		return "", "storage_error"
	}
	id := links.ProfileID(c.RecordID)
	if id == "" {
		id = keyPouchProfileID(site, c.RecordID)
	}
	match, reason := a.resolveKeyPouchProfile(c, id, "", links)
	if reason != "" {
		return "", reason
	}
	if match.candidate != "" {
		if !acceptExisting() {
			return "", "cancelled"
		}
		// Both the identity and the memberships can change while the dialog is open.
		match, reason = a.resolveKeyPouchProfile(c, id, match.candidate, links)
		if reason != "" {
			return "", reason
		}
	}
	if match.bound != "" {
		return match.bound, ""
	}
	if match.template == "" {
		template, err := a.BrowserProfileCreate(BrowserProfileInput{ProfileName: keyPouchTemplate})
		if err != nil {
			return "", "template_missing"
		}
		match.template = template.ProfileId
	}
	_, err := m.CopyBoundProfile(match.template, c.Email, id, c.GroupName)
	switch {
	case err == nil:
		return id, ""
	case errors.Is(err, browser.ErrBoundProfileConflict):
		return "", "profile_conflict"
	case errors.Is(err, browser.ErrAmbiguousGroupName):
		return "", "group_conflict"
	case errors.Is(err, browser.ErrRootGroupUnavailable):
		return "", "group_unavailable"
	default:
		return "", "create_failed"
	}
}

func (a *App) resolveKeyPouchProfile(c keyPouchOpenClaim, recordProfileID, confirmedID string, links *keypouch.ProfileLinks) (match keyPouchProfileMatch, reason string) {
	m := a.browserMgr
	m.Mutex.Lock()
	var updated *BrowserProfile
	defer func() {
		m.Mutex.Unlock()
		a.emitBrowserInstanceUpdated(updated)
	}()
	id := recordProfileID
	if confirmedID != "" {
		id = confirmedID
	}
	if !links.Available(c.RecordID, id) {
		return match, "profile_conflict"
	}
	profiles, err := m.ProfileDAO.List()
	if err != nil {
		return match, "storage_error"
	}
	deleted, err := m.ProfileDAO.ListDeleted()
	if err != nil {
		return match, "storage_error"
	}
	templateDeleted := false
	for _, p := range deleted {
		if p.ProfileId == id || p.ProfileId == recordProfileID {
			return match, "profile_unavailable"
		}
		templateDeleted = templateDeleted || p.ProfileName == keyPouchTemplate
	}
	for _, p := range profiles {
		if p.ProfileId == id {
			if p.ProfileName != c.Email {
				return match, "profile_conflict"
			}
			match.bound = p.ProfileId
		} else if p.ProfileId == recordProfileID {
			return match, "profile_conflict"
		} else if strings.EqualFold(p.ProfileName, c.Email) {
			if match.candidate != "" || p.ProfileName != c.Email {
				return match, "profile_conflict"
			}
			match.candidate = p.ProfileId
		}
		if p.ProfileName == keyPouchTemplate {
			if match.template != "" {
				return match, "profile_conflict"
			}
			match.template = p.ProfileId
		}
	}
	if match.bound == "" && (confirmedID != "" || links.ProfileID(c.RecordID) != "") {
		return match, "profile_unavailable"
	}
	if match.candidate != "" {
		uid, err := uuid.Parse(match.candidate)
		if match.bound != "" || !keyPouchLocalID.MatchString(match.candidate) ||
			(err == nil && uid.Version() == 5) || !links.Available(c.RecordID, match.candidate) {
			return match, "profile_conflict"
		}
		return match, ""
	}
	if match.bound == "" {
		if match.template == "" && templateDeleted {
			return match, "template_missing"
		}
		return match, ""
	}
	dao, ok := m.ProfileDAO.(*browser.SQLiteProfileDAO)
	if !ok {
		return match, "group_unavailable"
	}
	p, changed, err := dao.EnsureProfileRootGroup(id, c.GroupName)
	if errors.Is(err, browser.ErrAmbiguousGroupName) {
		return match, "group_conflict"
	}
	if err != nil {
		return match, "group_unavailable"
	}
	m.ApplyGroupSnapshotLocked([]*browser.Profile{p})
	if changed {
		updated = copyBrowserProfileSnapshot(m.Profiles[id])
	}
	if confirmedID != "" {
		if err := links.Bind(c.RecordID, id); err != nil {
			return match, "storage_error"
		}
	}
	return match, ""
}

func (a *App) keyPouchOpenProfile(ctx context.Context, site string, c keyPouchOpenClaim, device *keypouch.Device) (string, string) {
	links, err := device.LoadProfileLinks()
	if err != nil {
		return "", "storage_error"
	}
	return a.keyPouchProfile(site, c, links, func() bool { return confirmKeyPouchProfile(ctx, c) })
}

func (a *App) openKeyPouchWindow(ctx context.Context, id string, focusWindow func(int) bool) string {
	if _, err := a.BrowserInstanceStart(id); err != nil {
		return "open_failed"
	}
	// The native launcher can return while Chromium is still creating a window.
	// Do not acknowledge the open until the window can be brought forward.
	focus, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	var lastLookup time.Time
	detachedPID := 0
	openedWindow := false
	windowGrace := time.Now().Add(600 * time.Millisecond)
	for {
		a.browserMgr.Mutex.Lock()
		p := a.browserMgr.Profiles[id]
		port, pid := 0, 0
		dir := ""
		if p != nil && p.Running {
			port, pid = p.DebugPort, p.Pid
			dir = keyPouchDirKey(a.browserMgr.ResolveUserDataDir(p))
		}
		a.browserMgr.Mutex.Unlock()
		if runtime.GOOS == "windows" {
			if pid > 0 && focusWindow(pid) {
				return ""
			}
			// A packaged Chromium launcher can exit before its actual window appears.
			if dir != "" && time.Since(lastLookup) >= time.Second {
				lastLookup = time.Now()
				if dirs, err := keyPouchRunningDirs(focus); err == nil {
					detachedPID = dirs[dir].PID
				}
			}
			if detachedPID > 0 && detachedPID != pid && focusWindow(detachedPID) {
				return ""
			}
		} else if (pid > 0 && focusKeyPouchBrowserWindow(pid)) || (port > 0 && keyPouchFocusTab(focus, port) == nil) {
			return ""
		}
		// A background Chromium can be live with zero windows. The explicit
		// web action should open one, including on first launch while Chromium
		// is finishing its startup. Never add a tab to an existing window.
		if !openedWindow && port > 0 && time.Now().After(windowGrace) {
			if pages, err := keyPouchPageIDs(focus, port); err == nil && len(pages) == 0 {
				openedWindow = true
				_, _ = cdpBrowserCallResult(port, "Target.createTarget", map[string]any{"url": "about:blank", "newWindow": true})
			}
		}
		select {
		case <-focus.Done():
			return "focus_failed"
		case <-tick.C:
		}
	}
}

var keyPouchDebugClient = &http.Client{
	Timeout:       time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// An extension can keep Chromium and its debugging port alive after the last
// window closes. Only page targets represent open tabs.
func keyPouchPageIDs(ctx context.Context, port int) ([]string, error) {
	var targets []struct{ ID, Type, Subtype string }
	if err := fetchBrowserDebugJSONContext(ctx, keyPouchDebugClient, port, "/json/list", &targets); err != nil {
		return nil, err
	}
	if targets == nil {
		return nil, errors.New("无法读取实例标签页")
	}
	var pages []string
	for _, t := range targets {
		if t.Type == "page" && t.ID != "" && t.Subtype == "" {
			pages = append(pages, t.ID)
		}
	}
	return pages, nil
}

func keyPouchFocusTab(ctx context.Context, port int) error {
	pages, err := keyPouchPageIDs(ctx, port)
	if err != nil {
		return err
	}
	for _, id := range pages {
		addr := fmt.Sprintf("http://127.0.0.1:%d/json/activate/%s", port, url.PathEscape(id))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
		if err != nil {
			return err
		}
		res, err := keyPouchDebugClient.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 512))
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			return nil
		}
	}
	return errors.New("实例窗口尚未就绪")
}
