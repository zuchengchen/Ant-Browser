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

	"ant-chrome/backend/internal/keypouch"
	"github.com/google/uuid"
)

type keyPouchOpenClaim struct {
	Action    string `json:"action"`
	RecordID  int64  `json:"record_id"`
	Email     string `json:"email"`
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
}

const keyPouchTemplate = "KeyPouch 实例模板"

func keyPouchProfileID(recordID int64) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(keyPouchSite()+"/records/"+strconv.FormatInt(recordID, 10))).String()
}

func (a *App) keyPouchGroup(name, boundGroup string) (string, string) {
	groups, err := a.browserMgr.GroupDAO.List()
	if err != nil {
		return "", "group_unavailable"
	}
	var id string
	for _, g := range groups {
		if g.ParentId != "" || g.GroupName != name {
			continue
		}
		if id != "" {
			return "", "group_conflict"
		}
		id = g.GroupId
	}
	if boundGroup != "" && id != boundGroup {
		return "", "group_conflict"
	}
	if id != "" {
		return id, ""
	}
	g, err := a.CreateGroup(BrowserGroupInput{GroupName: name})
	if err != nil {
		return "", "group_unavailable"
	}
	return g.GroupId, ""
}

func (a *App) keyPouchProfile(c keyPouchOpenClaim, links *keypouch.ProfileLinks, acceptExisting func() bool) (string, string) {
	m := a.browserMgr
	if m == nil || m.ProfileDAO == nil || m.GroupDAO == nil {
		return "", "storage_error"
	}
	linked := links.ProfileID(c.RecordID)
	id := linked
	if id == "" {
		id = keyPouchProfileID(c.RecordID)
	}
	// Read through the DAOs to distinguish unavailable storage, deleted profiles,
	// and existing Ant profiles that happen to use the same email.
	profiles, err := m.ProfileDAO.List()
	if err != nil {
		return "", "storage_error"
	}
	deleted, err := m.ProfileDAO.ListDeleted()
	if err != nil {
		return "", "storage_error"
	}
	var bound, candidate, template *BrowserProfile
	templateDeleted := false
	for _, p := range deleted {
		if p.ProfileId == id {
			return "", "profile_unavailable"
		}
		templateDeleted = templateDeleted || p.ProfileName == keyPouchTemplate
	}
	for _, p := range profiles {
		if p.ProfileId == id {
			if p.ProfileName != c.Email {
				return "", "profile_conflict"
			}
			bound = p
		} else if strings.EqualFold(p.ProfileName, c.Email) {
			if candidate != nil || p.ProfileName != c.Email {
				return "", "profile_conflict"
			}
			candidate = p
		}
		if p.ProfileName == keyPouchTemplate {
			if template != nil {
				return "", "profile_conflict"
			}
			template = p
		}
	}
	if bound == nil && linked != "" {
		return "", "profile_unavailable"
	}
	if candidate != nil {
		if bound != nil {
			return "", "profile_conflict"
		}
		return a.adoptKeyPouchProfile(c, candidate, links, acceptExisting)
	}
	if bound == nil && template == nil && templateDeleted {
		return "", "template_missing"
	}
	boundGroup := ""
	if bound != nil {
		boundGroup = bound.GroupId
		if boundGroup == "" {
			return "", "group_conflict"
		}
	}
	group, reason := a.keyPouchGroup(c.GroupName, boundGroup)
	if reason != "" {
		return "", reason
	}
	if bound != nil {
		return id, ""
	}
	if template == nil {
		template, err = a.BrowserProfileCreate(BrowserProfileInput{ProfileName: keyPouchTemplate})
		if err != nil {
			return "", "template_missing"
		}
	}
	_, err = m.CopyBoundProfile(template.ProfileId, c.Email, id, group)
	if err != nil {
		return "", "create_failed"
	}
	return id, ""
}

// Match only the original local profile explicitly selected by the user. The
// site's profile-ID grammar also permits IDs from older Ant releases.
var keyPouchLocalID = regexp.MustCompile("^[A-Za-z0-9_-]{1,128}$")

func (a *App) adoptKeyPouchProfile(c keyPouchOpenClaim, p *BrowserProfile, links *keypouch.ProfileLinks, accept func() bool) (string, string) {
	uid, err := uuid.Parse(p.ProfileId)
	if !keyPouchLocalID.MatchString(p.ProfileId) || (err == nil && uid.Version() == 5) || !links.Available(c.RecordID, p.ProfileId) {
		return "", "profile_conflict"
	}
	if p.GroupId == "" {
		return "", "group_conflict"
	}
	if _, reason := a.keyPouchGroup(c.GroupName, p.GroupId); reason != "" {
		return "", reason
	}
	if !accept() {
		return "", "cancelled"
	}
	// The confirmation dialog may stay open while the local profile changes.
	current, err := a.browserMgr.ProfileDAO.GetById(p.ProfileId)
	if err != nil || current == nil || current.DeletedAt != "" {
		return "", "profile_unavailable"
	}
	if current.ProfileName != c.Email || current.GroupId != p.GroupId {
		return "", "profile_conflict"
	}
	if _, reason := a.keyPouchGroup(c.GroupName, current.GroupId); reason != "" {
		return "", reason
	}
	if err = links.Bind(c.RecordID, p.ProfileId); err != nil {
		return "", "storage_error"
	}
	return p.ProfileId, ""
}

func (a *App) keyPouchOpen(ctx context.Context, c keyPouchOpenClaim, device *keypouch.Device, focusWindow func(int) bool) (string, string) {
	links, err := device.LoadProfileLinks()
	if err != nil {
		return "", "storage_error"
	}
	id, reason := a.keyPouchProfile(c, links, func() bool { return confirmKeyPouchProfile(ctx, c) })
	if reason != "" {
		return "", reason
	}
	if _, err := a.BrowserInstanceStart(id); err != nil {
		return "", "open_failed"
	}
	// The native launcher can return while Chromium is still creating a window.
	// Do not acknowledge the open until the window can be brought forward.
	focus, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	var lastLookup time.Time
	detachedPID := 0
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
				return id, ""
			}
			// A packaged Chromium launcher can exit before its actual window appears.
			if dir != "" && time.Since(lastLookup) >= time.Second {
				lastLookup = time.Now()
				if dirs, err := keyPouchRunningDirs(focus); err == nil {
					detachedPID = dirs[dir].PID
				}
			}
			if detachedPID > 0 && detachedPID != pid && focusWindow(detachedPID) {
				return id, ""
			}
		} else if port > 0 && keyPouchFocusTab(focus, port) == nil {
			return id, ""
		}
		select {
		case <-focus.Done():
			return "", "focus_failed"
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
