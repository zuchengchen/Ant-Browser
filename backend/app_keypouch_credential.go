package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/keypouch"
)

var errGPTCredentialTarget = errors.New("无法确认 GPT 凭据所属的实例或内置扩展")

// The resolver calls this just before committing a bound profile's rename.
// The manager lock also excludes starts, credential refreshes and explicit clones.
// The caller runs restore under the same lock if the database commit fails.
func (a *App) retireRenamedKeyPouchCredentialLocked(ctx context.Context, p *BrowserProfile, site string, claim keyPouchOpenClaim) (restore func() error, err error) {
	if p == nil || p.DeletedAt != "" {
		return nil, errGPTCredentialTarget
	}
	old, err := a.readLocalGPTCredential(p.ProfileId)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if old.Site != site || old.RecordID != claim.RecordID {
			return nil, errGPTCredentialTarget
		}
		if old.Email == claim.Email {
			return nil, nil
		}
		next := localGPTCredential{Site: site, RecordID: claim.RecordID, Email: claim.Email}
		if err := next.Validate(); err != nil {
			return nil, err
		}
	}
	dir := a.browserMgr.ResolveUserDataDir(p)
	extension, err := gptSessionExtension(dir)
	if err != nil || old == nil && extension == "" {
		return nil, err
	}
	if !a.uniqueGPTProfileDirLocked(p) {
		return nil, errGPTSharedRuntime
	}
	port := 0
	if extension != "" {
		port, err = a.keyPouchCredentialSessionPortLocked(ctx, p, dir)
		if err != nil {
			return nil, err
		}
	}
	if old == nil {
		// No credential authorizes a marked session in this profile. This
		// also repairs an already-running browser after an interrupted open.
		if port != 0 {
			return nil, clearRecoveredGPTSession(ctx, port, dir, extension)
		}
		return nil, forgetGPTSessionExtension(dir)
	}

	restore = func() error {
		if err := a.saveLocalGPTCredential(p.ProfileId, *old); err != nil || port == 0 {
			return err
		}
		candidate := *p
		candidate.DebugPort = port
		// Re-verify the built-in files, Chromium directory and extension
		// even when a cancelled operation needs its old session restored.
		return a.publishGPTCredentialLocked(context.WithoutCancel(ctx), &candidate, []string{extension}, old)
	}
	if port != 0 {
		if err := clearRecoveredGPTSession(ctx, port, dir, extension); err != nil {
			// A lost storage reply or a failed marker unlink can follow an
			// effective removal. Earlier target checks have not touched Chrome.
			var markerErr *os.PathError
			if errors.Is(err, errGPTSessionWrite) || errors.As(err, &markerErr) {
				err = errors.Join(err, restore())
			}
			return nil, err
		}
	}
	// Clearing the live session first means a crash after removing the
	// vault file cannot leave the old login active in a retained Chromium.
	if err := keypouch.RemoveLocalGPTCredential(a.appRoot, p.ProfileId); err != nil {
		if port != 0 {
			err = errors.Join(err, restore())
		}
		return nil, err
	}
	return restore, nil
}

func (a *App) keyPouchCredentialSessionPortLocked(ctx context.Context, p *BrowserProfile, dir string) (int, error) {
	d := browserRuntimeDetection{PID: p.Pid, DebugPort: p.DebugPort}
	if !p.Running {
		// A marked browser can outlive Ant. Never interpret an unsuccessful
		// process scan as confirmation that the old login is gone.
		var live bool
		if d, live = detectBrowserRuntimeByActivePort(dir); !live {
			dirs, err := keyPouchRunningDirs(ctx)
			if err != nil {
				return 0, errGPTSessionRecovery
			}
			if d, live = dirs[keyPouchDirKey(dir)]; !live {
				return 0, nil
			}
		}
	}
	if d.DebugPort <= 0 {
		return 0, errGPTSessionRecovery
	}
	return d.DebugPort, a.checkGPTBrowserOwnerLocked(p, d)
}

func (a *App) reconcileKeyPouchCredential(ctx context.Context, id, site string, claim keyPouchOpenClaim) error {
	m := a.browserMgr
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	p := m.Profiles[id]
	if p == nil || p.ProfileName != claim.Email {
		return errGPTCredentialTarget
	}
	// An earlier version or an interrupted open may already have the new
	// display name. A failed/unsupported sync must still retire the old login.
	_, err := a.retireRenamedKeyPouchCredentialLocked(ctx, p, site, claim)
	return err
}

func (a *App) syncKeyPouchCredential(ctx context.Context, device *keypouch.Device, path, site string, claim keyPouchOpenClaim, id string) error {
	// A missing field is a protocol error. Only an explicit empty string clears an old value.
	var fields struct {
		RecordID int64   `json:"record_id"`
		Email    string  `json:"email"`
		Password *string `json:"password"`
		TOTP     *string `json:"totp_secret"`
	}
	input := map[string]any{"profile_id": id, "record_id": claim.RecordID, "email": claim.Email}
	if err := device.Post(ctx, path+"credentials", input, &fields); err != nil {
		return err
	}
	if fields.RecordID != claim.RecordID || fields.Email != claim.Email || fields.Password == nil || fields.TOTP == nil {
		return errors.New("KeyPouch 凭据响应不完整或记录已变更")
	}
	manager := a.browserMgr
	manager.Mutex.Lock()
	defer manager.Mutex.Unlock()
	p := manager.Profiles[id]
	if p == nil || p.DeletedAt != "" || p.ProfileName != claim.Email || !a.uniqueGPTProfileDirLocked(p) {
		return errGPTCredentialTarget
	}
	return a.saveLocalGPTCredential(id, localGPTCredential{
		Site: site, RecordID: claim.RecordID, Email: fields.Email, Password: *fields.Password, TOTPSecret: *fields.TOTP,
	})
}

func (a *App) uniqueGPTProfileDirLocked(p *BrowserProfile) bool {
	dir := a.browserMgr.ResolveUserDataDir(p)
	if active := a.runtimeUserDataDirLocked(p.ProfileId); p.Running && active != "" && !sameGPTDirectory(dir, active) {
		return false
	}
	for id, other := range a.browserMgr.Profiles {
		if id != p.ProfileId && other != nil && (sameGPTDirectory(dir, a.browserMgr.ResolveUserDataDir(other)) ||
			other.Running && sameGPTDirectory(dir, a.runtimeUserDataDirLocked(id))) {
			return false
		}
	}
	return dir != ""
}

func sameGPTDirectory(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if keyPouchDirKey(a) == keyPouchDirKey(b) {
		return true
	}
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	return err == nil && left.IsDir() && right.IsDir() && os.SameFile(left, right)
}

func (a *App) publishOpenedGPTCredential(ctx context.Context, id, site string, claim keyPouchOpenClaim) error {
	m := a.browserMgr
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	p := m.Profiles[id]
	if p == nil || p.DeletedAt != "" || !p.Running || p.ProfileName != claim.Email {
		return errGPTCredentialTarget
	}
	credential, err := a.readLocalGPTCredential(id)
	if err != nil || credential == nil || credential.Site != site || credential.RecordID != claim.RecordID || credential.Email != claim.Email {
		return errGPTCredentialTarget
	}
	return a.publishGPTCredentialLocked(ctx, p, m.ProfileExtensionLoadPaths(p), credential)
}

func (a *App) publishGPTCredentialLocked(ctx context.Context, p *BrowserProfile, paths []string, credential *localGPTCredential) error {
	if p.DebugPort <= 0 || !a.uniqueGPTProfileDirLocked(p) {
		return errGPTCredentialTarget
	}
	dir := a.resolveAppPath(filepath.Join("data", keyPouchExtensionDirectory))
	for _, path := range paths {
		if sameGPTDirectory(path, dir) {
			if err := browser.VerifyKeyPouchGPTFiles(dir); err != nil {
				return err
			}
			profileDir := a.browserMgr.ResolveUserDataDir(p)
			if err := rememberGPTSessionExtension(profileDir, dir); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return installGPTSession(ctx, p.DebugPort, profileDir, dir, credential)
		}
	}
	return errors.New("请为该实例启用内置的「登录 GPT」扩展")
}
