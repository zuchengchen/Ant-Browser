package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// A versioned migration is the only place legacy columns are authoritative.
// All validation and memberships are committed together with version 34.
func migrateInstanceGroups(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT group_id, group_name, COALESCE(parent_id, '') FROM browser_groups`)
	if err != nil {
		return err
	}
	parents := map[string]string{}
	roots := map[string]string{}
	siblings := map[[2]string]bool{}
	for rows.Next() {
		var id, name, parent string
		if err := rows.Scan(&id, &name, &parent); err != nil {
			rows.Close()
			return err
		}
		key := [2]string{parent, name}
		if siblings[key] || strings.TrimSpace(name) == "" {
			rows.Close()
			return fmt.Errorf("实例分组迁移中止：存在同级重名或空名称，请先处理分类冲突")
		}
		siblings[key] = true
		parents[id] = parent
		if parent == "" {
			roots[name] = id
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for id := range parents {
		seen := map[string]bool{}
		for current := id; current != ""; {
			if seen[current] {
				return fmt.Errorf("实例分组迁移中止：分组层级存在循环")
			}
			seen[current] = true
			parent, ok := parents[current]
			if !ok {
				return fmt.Errorf("实例分组迁移中止：存在缺失的父分组")
			}
			current = parent
		}
	}
	type legacyProfile struct {
		id, group string
		tags      []string
	}
	profiles := []legacyProfile{}
	rows, err = tx.Query(`SELECT profile_id, COALESCE(group_id, ''), COALESCE(tags, '[]') FROM browser_profiles`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p legacyProfile
		var tags string
		if err := rows.Scan(&p.id, &p.group, &tags); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(tags), &p.tags); err != nil {
			rows.Close()
			return fmt.Errorf("实例分组迁移中止：旧标签数据格式无效")
		}
		profiles = append(profiles, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, p := range profiles {
		ids := map[string]bool{}
		if p.group != "" {
			if _, ok := parents[p.group]; !ok {
				return fmt.Errorf("实例分组迁移中止：实例引用了不存在的分组")
			}
			ids[p.group] = true
		}
		for _, name := range p.tags {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("实例分组迁移中止：旧标签包含空名称")
			}
			id := roots[name]
			if id == "" {
				id = uuid.NewString()
				if _, err := tx.Exec(`INSERT INTO browser_groups (group_id, group_name, parent_id) VALUES (?, ?, '')`, id, name); err != nil {
					return err
				}
				roots[name] = id
			}
			ids[id] = true
		}
		for id := range ids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO browser_profile_groups (profile_id, group_id) VALUES (?, ?)`, p.id, id); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_browser_groups_sibling_name ON browser_groups(COALESCE(parent_id, ''), group_name)`)
	return err
}

func (db *DB) backupBeforeInstanceGroups() error {
	var count int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM browser_profiles`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if err := db.conn.QueryRow(`SELECT COUNT(*) FROM browser_groups`).Scan(&count); err != nil {
			return err
		}
	}
	if count == 0 {
		return nil
	}
	rows, err := db.conn.Query(`PRAGMA database_list`)
	if err != nil {
		return err
	}
	var filename string
	for rows.Next() {
		var seq int
		var name, path string
		if err := rows.Scan(&seq, &name, &path); err != nil {
			rows.Close()
			return err
		}
		if name == "main" {
			filename = path
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	// In-memory fixture databases have no persistent data to back up.
	if filename == "" {
		return nil
	}
	dir := filepath.Join(filepath.Dir(filename), "instance-group-backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	path, err := db.BackupTo(dir, "before-instance-groups-"+uuid.NewString())
	if err != nil {
		return fmt.Errorf("实例分组迁移前备份失败，已停止迁移: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	copyDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return err
	}
	defer copyDB.Close()
	var integrity string
	if err := copyDB.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("实例分组迁移前备份完整性验证失败，已停止迁移")
	}
	return nil
}
