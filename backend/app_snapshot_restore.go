package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"ant-chrome/backend/internal/logger"
	"ant-chrome/backend/internal/snapshot"
)

// snapshotRestore keeps the old directory intact until the complete archive has
// been extracted. Both renames stay on the destination filesystem.
type snapshotRestore struct {
	target           string
	root             string
	staged           string
	backup           string
	original         os.FileInfo
	preserveOriginal bool
	rename           func(string, string) error
}

func prepareSnapshotRestore(zipPath, target string) (_ *snapshotRestore, resultErr error) {
	target, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(target)
	if parent == target {
		return nil, fmt.Errorf("不能将文件系统根目录用作快照恢复目标")
	}
	original, err := snapshotRestoreDirectoryInfo(target)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("创建用户数据父目录失败: %w", err)
	}
	root, err := os.MkdirTemp(parent, ".snapshot-restore-*")
	if err != nil {
		return nil, fmt.Errorf("创建快照暂存目录失败: %w", err)
	}
	restore := &snapshotRestore{
		target:   target,
		root:     root,
		staged:   filepath.Join(root, "data"),
		backup:   filepath.Join(root, "original"),
		original: original,
		rename:   os.Rename,
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, restore.cleanup())
		}
	}()
	if err := os.Mkdir(restore.staged, 0o700); err != nil {
		return nil, err
	}
	if err := snapshot.UnzipTo(zipPath, restore.staged); err != nil {
		return nil, fmt.Errorf("解压快照失败，原数据未更改: %w", err)
	}
	mode := os.FileMode(0o755)
	if original != nil {
		mode = original.Mode().Perm()
	}
	if err := os.Chmod(restore.staged, mode); err != nil {
		return nil, fmt.Errorf("设置用户数据目录权限失败: %w", err)
	}
	return restore, nil
}

func snapshotRestoreDirectoryInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("检查用户数据目录失败: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("快照恢复目标必须为普通目录，不能是文件或符号链接: %s", path)
	}
	return info, nil
}

// commit must run while the browser manager lock excludes instance starts and
// profile changes. No cleanup may remove backup while it is the only old copy.
func (r *snapshotRestore) commit() error {
	current, err := snapshotRestoreDirectoryInfo(r.target)
	if err != nil {
		return err
	}
	if (r.original == nil) != (current == nil) ||
		(r.original != nil && !os.SameFile(r.original, current)) {
		return fmt.Errorf("用户数据目录在快照准备期间发生变化，请重试")
	}
	if r.original != nil {
		if err := r.rename(r.target, r.backup); err != nil {
			return fmt.Errorf("备份原用户数据目录失败，原数据未更改: %w", err)
		}
		r.preserveOriginal = true
	}
	if err := r.rename(r.staged, r.target); err != nil {
		if r.preserveOriginal {
			if rollbackErr := r.rename(r.backup, r.target); rollbackErr != nil {
				return fmt.Errorf("替换用户数据目录失败: %w；回滚失败: %w；原数据已保留在 %s", err, rollbackErr, r.backup)
			}
			r.preserveOriginal = false
		}
		return fmt.Errorf("替换用户数据目录失败，原数据未更改: %w", err)
	}
	// The replacement is installed. Old data is now a redundant backup and may
	// be removed by cleanup; cleanup errors must not be reported as a rollback.
	r.preserveOriginal = false
	return nil
}

func (r *snapshotRestore) cleanup() error {
	path := r.root
	if r.preserveOriginal {
		path = r.staged
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("清理快照暂存目录失败 (%s): %w", path, err)
	}
	return nil
}

func (a *App) restoreBrowserSnapshot(profileID, snapshotID string, prepare func(string, string) (*snapshotRestore, error)) (resultErr error) {
	// Use the same order as profile/package maintenance: maintenance, then
	// browser manager. Large decompression work does not hold the manager lock.
	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()

	a.browserMgr.Mutex.Lock()
	target, err := a.snapshotRestoreTargetLocked(profileID)
	a.browserMgr.Mutex.Unlock()
	if err != nil {
		return err
	}
	snapDir, err := a.snapshotDir(profileID)
	if err != nil {
		return err
	}
	_, zipPath, err := snapshot.FindFiles(snapDir, snapshotID)
	if err != nil {
		return err
	}
	restore, err := prepare(zipPath, target)
	if err != nil {
		return err
	}
	defer func() {
		if err := restore.cleanup(); err != nil {
			if resultErr != nil {
				resultErr = errors.Join(resultErr, err)
			} else {
				logger.New("Snapshot").Warn("快照已恢复，但暂存目录清理失败", logger.F("error", err.Error()))
			}
		}
	}()

	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	currentTarget, err := a.snapshotRestoreTargetLocked(profileID)
	if err != nil {
		return err
	}
	if currentTarget != target {
		return fmt.Errorf("实例用户数据目录在快照准备期间发生变化，请重试")
	}
	return restore.commit()
}

func (a *App) snapshotRestoreTargetLocked(profileID string) (string, error) {
	profile, exists := a.browserMgr.Profiles[profileID]
	if !exists || profile == nil || profile.DeletedAt != "" {
		return "", fmt.Errorf("实例不存在: %s", profileID)
	}
	target, err := filepath.Abs(a.browserMgr.ResolveUserDataDir(profile))
	if err != nil {
		return "", err
	}
	// Profiles may share an absolute user-data path. Protect every tracked
	// runtime using this directory, including processes pending debug attach.
	for id, candidate := range a.browserMgr.Profiles {
		if candidate == nil || normalizeProcessUserDataDir(a.browserMgr.ResolveUserDataDir(candidate)) != target {
			continue
		}
		tracked := a.browserMgr.BrowserProcesses[id]
		if candidate.Running || (candidate.Pid > 0 && isProcessAlive(candidate.Pid)) ||
			(tracked != nil && tracked.Process != nil && isProcessAlive(tracked.Process.Pid)) {
			return "", fmt.Errorf("请先停止实例再恢复快照")
		}
	}
	if _, live := detectBrowserRuntimeByActivePort(target); live {
		return "", fmt.Errorf("请先停止实例再恢复快照")
	}
	processes, err := findBrowserUserDataProcesses(target)
	if err != nil {
		return "", fmt.Errorf("无法确认实例已停止，取消快照恢复: %w", err)
	}
	if len(processes) > 0 {
		return "", fmt.Errorf("请先停止实例再恢复快照")
	}
	return target, nil
}
