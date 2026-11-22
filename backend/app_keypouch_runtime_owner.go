package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The marker contains only the unpacked built-in's installation path. Keep it
// before publishing so a lost CDP reply or an Ant restart cannot hide a live
// session. A newly created Chromium process starts with empty storage.session.
const gptSessionMarker = ".ant-gpt-session"

var errGPTSharedRuntime = errors.New("GPT 实例的数据目录或运行中的浏览器由多个配置使用，请为各实例设置独立目录并重新启动")
var errGPTSessionRecovery = errors.New("无法确认运行中实例的 GPT 登录凭据，请关闭该浏览器后重新启动实例")

func gptSessionExtension(dir string) (string, error) {
	path := filepath.Join(dir, gptSessionMarker)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 32768 {
		return "", errGPTSessionRecovery
	}
	data, err := os.ReadFile(path)
	extension := string(data)
	if err != nil || !filepath.IsAbs(extension) || filepath.Base(extension) != filepath.Base(keyPouchExtensionDirectory) {
		return "", errGPTSessionRecovery
	}
	return extension, nil
}

func rememberGPTSessionExtension(dir, extension string) error {
	if previous, err := gptSessionExtension(dir); err == nil && previous == extension {
		return nil
	}
	f, err := os.CreateTemp(dir, ".ant-gpt-session-*")
	if err != nil {
		return errGPTSessionRecovery
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(extension); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || os.Rename(f.Name(), filepath.Join(dir, gptSessionMarker)) != nil {
		return errGPTSessionRecovery
	}
	return nil
}

func (a *App) runtimeUserDataDirLocked(id string) string {
	if monitor := a.browserProcessMonitors[id]; monitor != nil {
		return monitor.userDataDir
	}
	return ""
}

// Ordinary profiles may share a directory. An encrypted credential or a live
// marked runtime makes that ambiguity unsafe for both the owner and the alias.
func (a *App) checkGPTBrowserOwnerLocked(p *BrowserProfile, detection browserRuntimeDetection) error {
	if a.appRoot == "" {
		return nil
	}
	m := a.browserMgr
	dir := m.ResolveUserDataDir(p)
	ownRuntime := a.runtimeUserDataDirLocked(p.ProfileId)
	if p.Running && ownRuntime != "" && !sameGPTDirectory(dir, ownRuntime) {
		previous, err := gptSessionExtension(ownRuntime)
		if err != nil || previous != "" {
			return errGPTSharedRuntime
		}
	}
	for id, other := range m.Profiles {
		if id == p.ProfileId || other == nil {
			continue
		}
		activeDir := a.runtimeUserDataDirLocked(id)
		sameRuntime := other.Running && (sameGPTDirectory(dir, activeDir) ||
			detection.DebugPort > 0 && other.DebugPort == detection.DebugPort ||
			detection.PID > 0 && other.Pid == detection.PID)
		if !sameRuntime && !sameGPTDirectory(dir, m.ResolveUserDataDir(other)) {
			continue
		}
		for _, candidate := range []string{id, p.ProfileId} {
			c, err := a.readLocalGPTCredential(candidate)
			if err != nil || c != nil {
				return errGPTSharedRuntime
			}
		}
		if sameRuntime {
			if activeDir == "" {
				activeDir = m.ResolveUserDataDir(other)
			}
			previous, err := gptSessionExtension(activeDir)
			if err != nil || previous != "" {
				return errGPTSharedRuntime
			}
		}
	}
	return nil
}

func (a *App) adoptDetectedBrowserLocked(ctx context.Context, p *BrowserProfile, d browserRuntimeDetection) error {
	if err := a.checkGPTBrowserOwnerLocked(p, d); err != nil {
		p.LastError = err.Error()
		return err
	}
	// Prepare the retained process before registering a launch slot, active
	// profile, window marker or URL action under the new profile ID.
	candidate := *p
	candidate.DebugPort = d.DebugPort
	warning, err := a.prepareProfileExtensionsLocked(ctx, &candidate, a.browserMgr.ProfileExtensionLoadPaths(p), true)
	if err != nil {
		p.LastError = errGPTSessionRecovery.Error()
		return errGPTSessionRecovery
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.markProfileLastLaunchArgsLocked(p, nil)
	a.markProfileRunningLocked(p.ProfileId, p, nil, d.PID, d.DebugPort, true, warning)
	return nil
}

func clearRecoveredGPTSession(ctx context.Context, port int, dir, extension string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := installGPTSession(ctx, port, dir, extension, nil); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(dir, gptSessionMarker))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
