package browser

import (
	"ant-chrome/backend/internal/keypouch"
	"ant-chrome/backend/internal/logger"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrProfileRunning = errors.New("实例正在运行或启动中，不能删除，请先停止实例")

// Delete 将配置移入回收站
func (m *Manager) Delete(profileId string) error {
	log := logger.New("Browser")
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()

	profile, exists := m.Profiles[profileId]
	if !exists {
		log.Error("浏览器配置不存在", logger.F("profile_id", profileId))
		return fmt.Errorf("profile not found")
	}
	if profile.Running || m.BrowserProcesses[profileId] != nil || m.profileHasRuntime(profile) {
		log.Warn("拒绝删除运行中的浏览器实例", logger.F("profile_id", profileId))
		return ErrProfileRunning
	}
	deletedAt := time.Now().Format(time.RFC3339)
	if m.ProfileDAO != nil {
		if err := m.ProfileDAO.SoftDelete(profileId, deletedAt); err != nil {
			log.Error("数据库移入回收站失败", logger.F("profile_id", profileId), logger.F("error", err))
			return err
		}
	} else {
		profile.DeletedAt = deletedAt
		profile.UpdatedAt = deletedAt
		delete(m.Profiles, profileId)
		if err := m.SaveProfiles(); err != nil {
			return err
		}
	}
	profile.DeletedAt = deletedAt
	profile.UpdatedAt = deletedAt
	delete(m.Profiles, profileId)
	resolvedDir := m.ResolveUserDataDir(profile)
	dataDirExists := pathExists(resolvedDir)
	fingerprintRoot := filepath.Join(m.ResolveRelativePath("data"), "fingerprint-check")
	fingerprintDir := filepath.Join(fingerprintRoot, safeProfilePathSegment(profile.ProfileId))
	if err := m.deleteProfileDirectoriesLocked(profile, []profileDirectoryLocation{{fingerprintDir, fingerprintRoot}}); err != nil {
		log.Error("删除实例指纹检测页缓存失败", logger.F("profile_id", profile.ProfileId), logger.F("error", err))
	}
	m.writeProfileDeleteAudit(log, profileDeleteAuditEntry{
		Action:               "soft_delete",
		ProfileID:            profile.ProfileId,
		ProfileName:          profile.ProfileName,
		UserDataDir:          profile.UserDataDir,
		ResolvedDir:          resolvedDir,
		DeletedAt:            deletedAt,
		DataDirExistedBefore: dataDirExists,
		DataDirExistsAfter:   dataDirExists,
		Success:              true,
	})
	log.Info("浏览器配置移入回收站", logger.F("profile_id", profileId))

	return nil
}

// ListDeleted 获取回收站实例
func (m *Manager) ListDeleted() []Profile {
	log := logger.New("Browser")
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	if m.ProfileDAO == nil {
		return []Profile{}
	}
	profiles, err := m.ProfileDAO.ListDeleted()
	if err != nil {
		log.Error("查询回收站实例失败", logger.F("error", err))
		return []Profile{}
	}
	list := make([]Profile, 0, len(profiles))
	for _, profile := range profiles {
		p := *profile
		if m.CodeProvider != nil {
			if code, err := m.CodeProvider.EnsureCode(p.ProfileId); err == nil {
				p.LaunchCode = code
			}
		}
		list = append(list, p)
	}
	return list
}

// Restore 从回收站恢复实例
func (m *Manager) Restore(profileId string) (*Profile, error) {
	log := logger.New("Browser")
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	if m.ProfileDAO == nil {
		return nil, fmt.Errorf("当前环境不支持回收站恢复")
	}
	profile, err := m.ProfileDAO.GetById(profileId)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(profile.DeletedAt) == "" {
		return nil, fmt.Errorf("实例不在回收站")
	}
	if err := m.ProfileDAO.Restore(profileId); err != nil {
		return nil, err
	}
	resolvedDir := m.ResolveUserDataDir(profile)
	dataDirExists := pathExists(resolvedDir)
	m.writeProfileDeleteAudit(log, profileDeleteAuditEntry{
		Action:               "restore",
		ProfileID:            profile.ProfileId,
		ProfileName:          profile.ProfileName,
		UserDataDir:          profile.UserDataDir,
		ResolvedDir:          resolvedDir,
		DeletedAt:            profile.DeletedAt,
		DataDirExistedBefore: dataDirExists,
		DataDirExistsAfter:   dataDirExists,
		Success:              true,
	})
	profile.DeletedAt = ""
	profile.UpdatedAt = time.Now().Format(time.RFC3339)
	profile.CoreId = normalizeProfileCoreID(profile.CoreId)
	m.Profiles[profile.ProfileId] = profile
	log.Info("实例已从回收站恢复", logger.F("profile_id", profileId))
	return profile, nil
}

// PermanentlyDelete 从回收站彻底删除实例及其关联数据
func (m *Manager) PermanentlyDelete(profileId string) error {
	log := logger.New("Browser")
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	if m.ProfileDAO == nil {
		return fmt.Errorf("当前环境不支持回收站物理删除")
	}
	profile, err := m.ProfileDAO.GetById(profileId)
	if err != nil {
		return err
	}
	if strings.TrimSpace(profile.DeletedAt) == "" {
		return fmt.Errorf("只能彻底删除回收站内的实例")
	}
	if m.BrowserProcesses[profileId] != nil || m.profileHasRuntime(profile) {
		log.Warn("拒绝彻底删除运行中的浏览器实例", logger.F("profile_id", profileId))
		return ErrProfileRunning
	}
	resolvedDir := m.ResolveUserDataDir(profile)
	dataDirExistedBefore := pathExists(resolvedDir)
	if err := m.deleteProfileRelatedDataLocked(log, profile); err != nil {
		m.writeProfileDeleteAudit(log, profileDeleteAuditEntry{
			Action:               "permanent_delete",
			ProfileID:            profile.ProfileId,
			ProfileName:          profile.ProfileName,
			UserDataDir:          profile.UserDataDir,
			ResolvedDir:          resolvedDir,
			DeletedAt:            profile.DeletedAt,
			DataDirExistedBefore: dataDirExistedBefore,
			DataDirExistsAfter:   pathExists(resolvedDir),
			Success:              false,
			Error:                err.Error(),
		})
		return err
	}
	if err := m.ProfileDAO.Delete(profileId); err != nil {
		m.writeProfileDeleteAudit(log, profileDeleteAuditEntry{
			Action:               "permanent_delete",
			ProfileID:            profile.ProfileId,
			ProfileName:          profile.ProfileName,
			UserDataDir:          profile.UserDataDir,
			ResolvedDir:          resolvedDir,
			DeletedAt:            profile.DeletedAt,
			DataDirExistedBefore: dataDirExistedBefore,
			DataDirExistsAfter:   pathExists(resolvedDir),
			Success:              false,
			Error:                err.Error(),
		})
		return err
	}
	m.writeProfileDeleteAudit(log, profileDeleteAuditEntry{
		Action:               "permanent_delete",
		ProfileID:            profile.ProfileId,
		ProfileName:          profile.ProfileName,
		UserDataDir:          profile.UserDataDir,
		ResolvedDir:          resolvedDir,
		DeletedAt:            profile.DeletedAt,
		DataDirExistedBefore: dataDirExistedBefore,
		DataDirExistsAfter:   pathExists(resolvedDir),
		Success:              true,
	})
	log.Info("回收站实例已彻底删除", logger.F("profile_id", profileId))
	return nil
}

func (m *Manager) deleteProfileRelatedDataLocked(log *logger.Logger, profile *Profile) error {
	if profile == nil {
		return nil
	}
	directories, err := m.profileDirectoriesForRemoval(profile, m.profileDirectoryLocations(profile))
	if err != nil {
		return err
	}
	var firstErr error
	if m.CodeProvider != nil {
		if err := m.CodeProvider.Remove(profile.ProfileId); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if m.ExtensionDAO != nil {
		if err := m.ExtensionDAO.DeleteProfileSettings(profile.ProfileId); err != nil {
			log.Error("删除实例插件配置失败", logger.F("profile_id", profile.ProfileId), logger.F("error", err))
			if firstErr == nil {
				firstErr = err
			}
		}
		if err := m.ExtensionDAO.DeleteProfileExtensionRuntimeForProfile(profile.ProfileId); err != nil {
			log.Error("删除实例插件运行态失败", logger.F("profile_id", profile.ProfileId), logger.F("error", err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	for _, directory := range directories {
		if err := os.RemoveAll(directory); err != nil {
			log.Error("删除实例目录失败", logger.F("profile_id", profile.ProfileId), logger.F("dir", directory), logger.F("error", err))
			if firstErr == nil {
				firstErr = fmt.Errorf("删除实例目录失败: %w", err)
			}
		}
	}
	if firstErr == nil && m.AppRoot != "" {
		firstErr = keypouch.RemoveLocalGPTCredential(m.AppRoot, profile.ProfileId)
	}
	return firstErr
}

func (m *Manager) deleteProfileDirectoriesLocked(profile *Profile, locations []profileDirectoryLocation) error {
	paths, err := m.profileDirectoriesForRemoval(profile, locations)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

type profileDirectory struct {
	path     string
	resolved string
}

type profileDirectoryLocation struct {
	path string
	root string
}

// profileDirectoriesForRemoval runs under Mutex before any part of a profile is removed.
func (m *Manager) profileDirectoriesForRemoval(profile *Profile, locations []profileDirectoryLocation) ([]string, error) {
	var candidates []profileDirectory
	for _, location := range locations {
		directory, err := resolveProfileDirectory(location.path)
		if err != nil {
			return nil, fmt.Errorf("检查待删除目录失败: %w", err)
		}
		if directory.resolved == "" {
			continue
		}
		root, err := resolveProfileDirectory(location.root)
		if err != nil {
			return nil, fmt.Errorf("检查实例目录根路径失败: %w", err)
		}
		if !isPathInside(directory.path, root.path) || root.resolved == "" || !isPathInside(directory.resolved, root.resolved) || samePath(directory.resolved, root.resolved) {
			continue
		}
		candidates = append(candidates, directory)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	others := make(map[string]*Profile)
	for id, other := range m.Profiles {
		others[id] = other
	}
	if m.ProfileDAO != nil {
		active, err := m.ProfileDAO.List()
		if err != nil {
			return nil, fmt.Errorf("无法核对其他实例的数据目录: %w", err)
		}
		deleted, err := m.ProfileDAO.ListDeleted()
		if err != nil {
			return nil, fmt.Errorf("无法核对回收站实例的数据目录: %w", err)
		}
		for _, other := range append(active, deleted...) {
			if other != nil {
				others[other.ProfileId] = other
			}
		}
	}
	var protected []profileDirectory
	for id, other := range others {
		if other == nil || id == profile.ProfileId {
			continue
		}
		for _, location := range m.profileDirectoryLocations(other) {
			directory, err := resolveProfileDirectory(location.path)
			if err != nil {
				return nil, fmt.Errorf("无法确认其他实例的数据目录: %w", err)
			}
			protected = append(protected, directory)
		}
	}

	var paths []string
	for _, directory := range candidates {
		shared := false
		for _, other := range protected {
			if profileDirectoriesOverlap(directory.path, other.path) || (other.resolved != "" && profileDirectoriesOverlap(directory.resolved, other.resolved)) {
				shared = true
				break
			}
		}
		if !shared {
			paths = append(paths, directory.path)
		}
	}
	return paths, nil
}

func (m *Manager) profileDirectoryLocations(profile *Profile) []profileDirectoryLocation {
	root := strings.TrimSpace(m.Config.Browser.UserDataRoot)
	if root == "" {
		root = "data"
	}
	dataRoot := m.ResolveRelativePath("data")
	snapshotRoot := filepath.Join(dataRoot, "snapshots")
	fingerprintRoot := filepath.Join(dataRoot, "fingerprint-check")
	return []profileDirectoryLocation{
		{m.ResolveUserDataDir(profile), m.ResolveRelativePath(root)},
		{filepath.Join(snapshotRoot, profile.ProfileId), snapshotRoot},
		{filepath.Join(fingerprintRoot, safeProfilePathSegment(profile.ProfileId)), fingerprintRoot},
	}
}

func resolveProfileDirectory(path string) (profileDirectory, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return profileDirectory{}, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if os.IsNotExist(err) {
		return profileDirectory{path: absolute}, nil
	}
	return profileDirectory{path: absolute, resolved: resolved}, err
}

func profileDirectoriesOverlap(a, b string) bool {
	// A case-insensitive data volume can be mounted on any supported OS.
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a == b || isPathInside(a, b) || isPathInside(b, a)
}

func safeProfilePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func samePath(a string, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func isPathInside(path string, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil || rel == "." || rel == "" {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
