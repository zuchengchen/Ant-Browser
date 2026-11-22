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
	"ant-chrome/backend/internal/logger"
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
	return a.keyPouchProfileWithContext(context.Background(), site, c, links, acceptExisting)
}

func (a *App) keyPouchProfileWithContext(ctx context.Context, site string, c keyPouchOpenClaim, links *keypouch.ProfileLinks, acceptExisting func() bool) (string, string) {
	m := a.browserMgr
	if m == nil || m.ProfileDAO == nil || m.GroupDAO == nil {
		return "", "storage_error"
	}
	m.InitData()
	id := keyPouchProfileID(site, c.RecordID)
	match, reason := a.resolveKeyPouchProfile(ctx, site, c, id, "", links)
	if reason != "" {
		return "", reason
	}
	if match.candidate != "" {
		if !acceptExisting() {
			return "", "cancelled"
		}
		// Both the identity and the memberships can change while the dialog is open.
		match, reason = a.resolveKeyPouchProfile(ctx, site, c, id, match.candidate, links)
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

func (a *App) resolveKeyPouchProfile(ctx context.Context, site string, c keyPouchOpenClaim, recordProfileID, confirmedID string, links *keypouch.ProfileLinks) (match keyPouchProfileMatch, reason string) {
	m := a.browserMgr
	m.Mutex.Lock()
	var updated *BrowserProfile
	defer func() {
		m.Mutex.Unlock()
		a.emitBrowserInstanceUpdated(updated)
	}()
	id := recordProfileID
	if linked := links.ProfileID(c.RecordID); linked != "" {
		id = linked
	}
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
	var bound *BrowserProfile
	for _, p := range deleted {
		if p.ProfileId == id || p.ProfileId == recordProfileID {
			return match, "profile_unavailable"
		}
		templateDeleted = templateDeleted || p.ProfileName == keyPouchTemplate
	}
	for _, p := range profiles {
		if p.ProfileId == id {
			// A newly confirmed adoption must still identify the same local
			// account the user saw in the dialog. Earlier bindings use their ID.
			if confirmedID != "" && p.ProfileName != c.Email {
				return match, "profile_conflict"
			}
			bound = p
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
		if _, reason := a.keyPouchProfileCredentialEmail(match.candidate, site, c.RecordID); reason != "" {
			return match, reason
		}
		return match, ""
	}
	if match.bound == "" {
		if match.template == "" && templateDeleted {
			return match, "template_missing"
		}
		return match, ""
	}
	updated, reason = a.updateKeyPouchBoundProfileLocked(ctx, site, c, bound)
	if reason != "" {
		return match, reason
	}
	if confirmedID != "" {
		if err := links.Bind(c.RecordID, id); err != nil {
			return match, "storage_error"
		}
	}
	return match, ""
}

func (a *App) keyPouchProfileCredentialEmail(id, site string, recordID int64) (string, string) {
	credential, err := a.readLocalGPTCredential(id)
	if err != nil {
		return "", "storage_error"
	}
	if credential == nil {
		return "", ""
	}
	if credential.Site != site || credential.RecordID != recordID {
		return "", "profile_conflict"
	}
	return credential.Email, ""
}

func (a *App) updateKeyPouchBoundProfileLocked(ctx context.Context, site string, c keyPouchOpenClaim, bound *BrowserProfile) (*BrowserProfile, string) {
	m := a.browserMgr
	dao, ok := m.ProfileDAO.(*browser.SQLiteProfileDAO)
	if !ok {
		return nil, "group_unavailable"
	}
	previousEmail, reason := a.keyPouchProfileCredentialEmail(bound.ProfileId, site, c.RecordID)
	if reason != "" {
		return nil, reason
	}
	current := m.Profiles[bound.ProfileId]
	if current == nil {
		current = bound
	}
	var rename *browser.BoundProfileRename
	var restore func() error
	var retireErr error
	if bound.ProfileName != c.Email {
		// A previous signed snapshot distinguishes a remote rename from a
		// locally edited display name. Old open-only profiles have no snapshot.
		if bound.ProfileName == keyPouchTemplate || previousEmail == c.Email {
			return nil, "profile_conflict"
		}
		rename = &browser.BoundProfileRename{From: bound.ProfileName, To: c.Email, BeforeCommit: func() error {
			restore, retireErr = a.retireRenamedKeyPouchCredentialLocked(ctx, current, site, c)
			return retireErr
		}}
	}
	p, changed, err := dao.UpdateBoundProfile(bound.ProfileId, c.GroupName, rename)
	switch {
	case err == nil:
		if m.Profiles[p.ProfileId] == nil {
			m.Profiles[p.ProfileId] = p
		} else {
			current.ProfileName = p.ProfileName
			m.ApplyGroupSnapshotLocked([]*browser.Profile{p})
		}
		if changed {
			return copyBrowserProfileSnapshot(m.Profiles[p.ProfileId]), ""
		}
		return nil, ""
	case retireErr != nil:
		return nil, "storage_error"
	case restore != nil:
		// Only the final SQLite commit could have failed after retirement.
		if restore() != nil {
			logger.New("KeyPouch").Warn("实例改名未提交，恢复该实例的原登录凭据或扩展会话失败")
		}
		return nil, "storage_error"
	case errors.Is(err, browser.ErrBoundProfileConflict):
		return nil, "profile_conflict"
	case errors.Is(err, browser.ErrAmbiguousGroupName):
		return nil, "group_conflict"
	default:
		return nil, "group_unavailable"
	}
}

func (a *App) keyPouchOpenProfile(ctx context.Context, site string, c keyPouchOpenClaim, device *keypouch.Device) (string, string) {
	links, err := device.LoadProfileLinks()
	if err != nil {
		return "", "storage_error"
	}
	return a.keyPouchProfileWithContext(ctx, site, c, links, func() bool { return confirmKeyPouchProfile(ctx, c) })
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
