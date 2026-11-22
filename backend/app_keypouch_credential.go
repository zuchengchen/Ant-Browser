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

func (a *App) publishOpenedGPTCredential(ctx context.Context, id string) error {
	m := a.browserMgr
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	p := m.Profiles[id]
	if p == nil || p.DeletedAt != "" || !p.Running {
		return errGPTCredentialTarget
	}
	credential, err := a.readLocalGPTCredential(id)
	if err != nil || credential == nil {
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
