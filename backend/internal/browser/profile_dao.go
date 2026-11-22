package browser

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProfileDAO 实例配置持久化接口
type ProfileDAO interface {
	List() ([]*Profile, error)
	ListDeleted() ([]*Profile, error)
	GetById(profileId string) (*Profile, error)
	Upsert(profile *Profile) error
	Delete(profileId string) error
	SoftDelete(profileId string, deletedAt string) error
	Restore(profileId string) error
	ListExpiredDeleted(expiredBefore string) ([]*Profile, error)
}

// ProfileGroupCounter is optional so alternate ProfileDAO implementations keep
// their existing contract. Counts always reflect persisted, non-deleted rows.
type ProfileGroupCounter interface {
	CountByGroup() (map[string]int, error)
}

// SQLiteProfileDAO 基于 SQLite 的 ProfileDAO 实现
type SQLiteProfileDAO struct {
	db *sql.DB
}

// NewSQLiteProfileDAO 创建 SQLiteProfileDAO
func NewSQLiteProfileDAO(db *sql.DB) *SQLiteProfileDAO {
	return &SQLiteProfileDAO{db: db}
}

// profileSelect derives compatibility fields from the membership relation.
// Correlated indexed subqueries execute in one SQLite query, without N+1 RPCs.
const profileSelect = `SELECT profile_id, profile_name, user_data_dir, core_id,
 fingerprint_args, proxy_id, proxy_config,
 COALESCE(proxy_mode, ''), COALESCE(proxy_group_name, ''),
 COALESCE(proxy_bind_source_id, ''), COALESCE(proxy_bind_source_url, ''),
 COALESCE(proxy_bind_name, ''), COALESCE(proxy_bind_updated_at, ''), COALESCE(proxy_bind_error, ''),
 COALESCE(memory_limit_mb, 0), launch_args,
 (SELECT json_group_array(group_name) FROM (
   SELECT g.group_name FROM browser_profile_groups m JOIN browser_groups g ON g.group_id=m.group_id
   WHERE m.profile_id=browser_profiles.profile_id ORDER BY m.group_id)),
 keywords,
 COALESCE((SELECT m.group_id FROM browser_profile_groups m
   WHERE m.profile_id=browser_profiles.profile_id AND m.group_id=browser_profiles.group_id),
   (SELECT MIN(m.group_id) FROM browser_profile_groups m WHERE m.profile_id=browser_profiles.profile_id), ''),
 created_at, updated_at, COALESCE(restore_last_session, ''), COALESCE(deleted_at, ''),
 (SELECT json_group_array(group_id) FROM (
   SELECT m.group_id FROM browser_profile_groups m WHERE m.profile_id=browser_profiles.profile_id ORDER BY m.group_id))
 FROM browser_profiles`

func (d *SQLiteProfileDAO) CountByGroup() (map[string]int, error) {
	rows, err := d.db.Query(`WITH RECURSIVE descendants(root, id) AS (
   SELECT group_id, group_id FROM browser_groups
   UNION SELECT d.root, g.group_id FROM descendants d JOIN browser_groups g ON g.parent_id=d.id
 ) SELECT d.root, COUNT(DISTINCT p.profile_id) FROM descendants d
 JOIN browser_profile_groups m ON m.group_id=d.id
 JOIN browser_profiles p ON p.profile_id=m.profile_id
 WHERE COALESCE(p.deleted_at, '')='' GROUP BY d.root`)
	if err != nil {
		return nil, fmt.Errorf("统计分组实例数量失败: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

func (d *SQLiteProfileDAO) queryProfiles(where string, args ...any) ([]*Profile, error) {
	rows, err := d.db.Query(profileSelect+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := []*Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}
func (d *SQLiteProfileDAO) List() ([]*Profile, error) {
	return d.queryProfiles("WHERE COALESCE(deleted_at, '')='' ORDER BY created_at ASC")
}
func (d *SQLiteProfileDAO) ListDeleted() ([]*Profile, error) {
	return d.queryProfiles("WHERE COALESCE(deleted_at, '')!='' ORDER BY deleted_at DESC")
}
func (d *SQLiteProfileDAO) GetById(profileId string) (*Profile, error) {
	p, err := scanProfile(d.db.QueryRow(profileSelect+" WHERE profile_id=?", profileId))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("实例不存在: %s", profileId)
	}
	return p, err
}

// Reconciliation owns only these binding fields. Older profiles can contain
// JSON representations that the normal frontend read projection normalizes.
func (d *SQLiteProfileDAO) UpdateProxyBinding(p *Profile) error {
	result, err := d.db.Exec(`UPDATE browser_profiles SET proxy_id=?,proxy_config=?,proxy_bind_source_id=?,proxy_bind_source_url=?,
		proxy_bind_name=?,proxy_bind_updated_at=?,proxy_bind_error=?,updated_at=? WHERE profile_id=? AND COALESCE(deleted_at,'')=''`,
		p.ProxyId, p.ProxyConfig, p.ProxyBindSourceID, p.ProxyBindSourceURL, p.ProxyBindName, p.ProxyBindUpdatedAt, p.ProxyBindError, p.UpdatedAt, p.ProfileId)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return errors.New("实例不存在或已在回收站")
	}
	return err
}

// Upsert 新增或更新实例配置
func (d *SQLiteProfileDAO) Upsert(profile *Profile) error {
	return d.saveProfile(profile, nil)
}

var ErrBoundProfileConflict = errors.New("目标实例已存在或同名实例发生变更")

// The user may have changed the local catalog since the initial KeyPouch
// lookup. Match with the same Unicode case folding used for existing profiles.
func checkNewBoundProfile(tx *sql.Tx, profile *Profile) error {
	rows, err := tx.Query(`SELECT profile_id,profile_name FROM browser_profiles WHERE profile_id=? OR COALESCE(deleted_at,'')=''`, profile.ProfileId)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		if id == profile.ProfileId || strings.EqualFold(name, profile.ProfileName) {
			return ErrBoundProfileConflict
		}
	}
	return rows.Err()
}

// A named root is supplied only for a new bound copy. Resolve it inside the
// insert transaction so a concurrent rename or failed copy cannot misclassify it.
func (d *SQLiteProfileDAO) saveProfile(profile *Profile, newRootGroup *string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	next := *profile
	if newRootGroup != nil {
		if err := checkNewBoundProfile(tx, &next); err != nil {
			return err
		}
		id, err := ensureRootGroup(tx, *newRootGroup)
		if err != nil {
			return err
		}
		next.GroupId, next.GroupIds = id, []string{id}
	}
	ids, err := resolveProfileGroupInput(tx, &next)
	if err != nil {
		return err
	}

	fingerprintArgs, _ := json.Marshal(next.FingerprintArgs)
	launchArgs, _ := json.Marshal(next.LaunchArgs)
	tags, _ := json.Marshal(next.Tags)
	keywords, _ := json.Marshal(next.Keywords)

	now := time.Now().Format(time.RFC3339)
	if next.CreatedAt == "" {
		next.CreatedAt = now
	}
	if next.UpdatedAt == "" {
		next.UpdatedAt = now
	}

	statement := `
		INSERT INTO browser_profiles
		  (profile_id, profile_name, user_data_dir, core_id, fingerprint_args,
		   proxy_id, proxy_config, proxy_mode, proxy_group_name, proxy_bind_source_id, proxy_bind_source_url, proxy_bind_name, proxy_bind_updated_at, proxy_bind_error,
		   memory_limit_mb, launch_args, tags, keywords, group_id, created_at, updated_at, restore_last_session, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if newRootGroup == nil {
		statement += ` ON CONFLICT(profile_id) DO UPDATE SET
		  profile_name     = excluded.profile_name,
		  user_data_dir    = excluded.user_data_dir,
		  core_id          = excluded.core_id,
		  fingerprint_args = excluded.fingerprint_args,
		  proxy_id         = excluded.proxy_id,
		  proxy_config     = excluded.proxy_config,
		  proxy_mode       = excluded.proxy_mode,
		  proxy_group_name = excluded.proxy_group_name,
		  proxy_bind_source_id = excluded.proxy_bind_source_id,
		  proxy_bind_source_url = excluded.proxy_bind_source_url,
		  proxy_bind_name = excluded.proxy_bind_name,
		  proxy_bind_updated_at = excluded.proxy_bind_updated_at,
		  proxy_bind_error = excluded.proxy_bind_error,
		  memory_limit_mb  = excluded.memory_limit_mb,
		  launch_args      = excluded.launch_args,
		  tags             = excluded.tags,
		  keywords         = excluded.keywords,
		  group_id         = excluded.group_id,
		  restore_last_session = excluded.restore_last_session,
		  deleted_at       = excluded.deleted_at,
		  updated_at       = excluded.updated_at`
	}
	_, err = tx.Exec(statement,
		next.ProfileId, next.ProfileName, next.UserDataDir, next.CoreId,
		string(fingerprintArgs), next.ProxyId, next.ProxyConfig,
		NormalizeProxyMode(next.ProxyMode), NormalizeProxyGroupName(next.ProxyGroupName),
		next.ProxyBindSourceID, next.ProxyBindSourceURL, next.ProxyBindName, next.ProxyBindUpdatedAt, next.ProxyBindError,
		normalizeMemoryLimitMB(next.MemoryLimitMB), string(launchArgs), string(tags), string(keywords), next.GroupId,
		next.CreatedAt, next.UpdatedAt, NormalizeRestoreLastSessionMode(next.RestoreLastSession), next.DeletedAt,
	)
	if err != nil {
		return fmt.Errorf("保存实例配置失败: %w", err)
	}
	if err := WriteProfileGroupMemberships(tx, &next, ids); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*profile = next
	return nil
}

// SoftDelete 将实例移入回收站
func (d *SQLiteProfileDAO) SoftDelete(profileId string, deletedAt string) error {
	result, err := d.db.Exec(`UPDATE browser_profiles SET deleted_at = ?, updated_at = ? WHERE profile_id = ?`, deletedAt, deletedAt, profileId)
	if err != nil {
		return fmt.Errorf("移入回收站失败: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("实例不存在: %s", profileId)
	}
	return nil
}

// Restore 从回收站恢复实例
func (d *SQLiteProfileDAO) Restore(profileId string) error {
	now := time.Now().Format(time.RFC3339)
	result, err := d.db.Exec(`UPDATE browser_profiles SET deleted_at = '', updated_at = ? WHERE profile_id = ?`, now, profileId)
	if err != nil {
		return fmt.Errorf("恢复实例失败: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("实例不存在: %s", profileId)
	}
	return nil
}

// ListExpiredDeleted reads the same canonical memberships as normal queries.
func (d *SQLiteProfileDAO) ListExpiredDeleted(expiredBefore string) ([]*Profile, error) {
	return d.queryProfiles("WHERE COALESCE(deleted_at, '')!='' AND deleted_at<=?", expiredBefore)
}

// Delete 删除实例配置
func (d *SQLiteProfileDAO) Delete(profileId string) error {
	_, err := d.db.Exec(`DELETE FROM browser_profiles WHERE profile_id = ?`, profileId)
	if err != nil {
		return fmt.Errorf("删除实例配置失败: %w", err)
	}
	return nil
}

// ListByGroup returns each instance once even when it belongs to several descendants.
func (d *SQLiteProfileDAO) ListByGroup(groupId string, includeChildren bool, childGroupIds []string) ([]*Profile, error) {
	if groupId == "" {
		return d.queryProfiles("WHERE COALESCE(deleted_at, '')='' AND NOT EXISTS (SELECT 1 FROM browser_profile_groups m WHERE m.profile_id=browser_profiles.profile_id) ORDER BY created_at")
	}
	ids := []string{groupId}
	if includeChildren {
		ids = append(ids, childGroupIds...)
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	return d.queryProfiles("WHERE COALESCE(deleted_at, '')='' AND EXISTS (SELECT 1 FROM browser_profile_groups m WHERE m.profile_id=browser_profiles.profile_id AND m.group_id IN ("+strings.Join(marks, ",")+")) ORDER BY created_at", args...)
}

// MoveToGroup is a legacy additive operation; it never discards other memberships.
func (d *SQLiteProfileDAO) MoveToGroup(profileIds []string, groupId string) error {
	if groupId == "" {
		return fmt.Errorf("清空分组请使用显式的多个分组设置接口")
	}
	return d.ChangeGroups(profileIds, []string{groupId}, "add")
}

// scanner 统一扫描接口，兼容 *sql.Row 和 *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanProfile(s scanner) (*Profile, error) {
	var (
		fingerprintArgsJSON, launchArgsJSON, tagsJSON, keywordsJSON, groupIDsJSON string
		p                                                                         Profile
	)
	err := s.Scan(
		&p.ProfileId, &p.ProfileName, &p.UserDataDir, &p.CoreId,
		&fingerprintArgsJSON, &p.ProxyId, &p.ProxyConfig,
		&p.ProxyMode, &p.ProxyGroupName,
		&p.ProxyBindSourceID, &p.ProxyBindSourceURL, &p.ProxyBindName, &p.ProxyBindUpdatedAt, &p.ProxyBindError,
		&p.MemoryLimitMB, &launchArgsJSON, &tagsJSON, &keywordsJSON, &p.GroupId,
		&p.CreatedAt, &p.UpdatedAt, &p.RestoreLastSession, &p.DeletedAt, &groupIDsJSON,
	)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(fingerprintArgsJSON), &p.FingerprintArgs)
	_ = json.Unmarshal([]byte(launchArgsJSON), &p.LaunchArgs)
	_ = json.Unmarshal([]byte(tagsJSON), &p.Tags)
	if err := json.Unmarshal([]byte(groupIDsJSON), &p.GroupIds); err != nil {
		return nil, err
	}
	if p.GroupIds == nil {
		p.GroupIds = []string{}
	}
	_ = json.Unmarshal([]byte(keywordsJSON), &p.Keywords)
	if p.FingerprintArgs == nil {
		p.FingerprintArgs = []string{}
	}
	if p.LaunchArgs == nil {
		p.LaunchArgs = []string{}
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if p.Keywords == nil {
		p.Keywords = []string{}
	}
	p.RestoreLastSession = NormalizeRestoreLastSessionMode(p.RestoreLastSession)
	p.ProxyMode = NormalizeProxyMode(p.ProxyMode)
	p.ProxyGroupName = NormalizeProxyGroupName(p.ProxyGroupName)
	p.MemoryLimitMB = normalizeMemoryLimitMB(p.MemoryLimitMB)
	return &p, nil
}
