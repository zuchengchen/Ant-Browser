package backend

import (
	"ant-chrome/backend/internal/browser"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Merge canonical memberships after both ID maps and parent rows exist. A
// matched existing profile receives the union, not only newly imported rows.
func backupMergeInstanceGroups(tx *sql.Tx, mappings *backupImportReferenceMappings) error {
	exists, err := backupSrcTableExists(tx, "browser_profiles")
	if err != nil {
		return err
	}
	canonical, err := backupSrcTableExists(tx, "browser_profile_groups")
	if err != nil {
		return err
	}
	if !exists {
		if canonical {
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM src.browser_profile_groups`).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("备份缺少实例表但仍含分组归属，导入已取消")
			}
		}
		return nil
	}
	groupExpr, err := backupSourceColumnExpression(tx, "browser_profiles", "s", "group_id", "''")
	if err != nil {
		return err
	}
	tagsExpr, err := backupSourceColumnExpression(tx, "browser_profiles", "s", "tags", "'[]'")
	if err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT s.profile_id,` + groupExpr + `,` + tagsExpr + ` FROM src.browser_profiles s`)
	if err != nil {
		return err
	}
	type incoming struct {
		id, group, tags string
		ids             []string
	}
	profiles := []incoming{}
	for rows.Next() {
		var p incoming
		if err := rows.Scan(&p.id, &p.group, &p.tags); err != nil {
			rows.Close()
			return err
		}
		profiles = append(profiles, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	sourceProfiles := make(map[string]bool, len(profiles))
	normalizedProfiles := map[string]bool{}
	for _, profile := range profiles {
		key := backupImportIDKey(profile.id)
		if key == "" || normalizedProfiles[key] {
			return fmt.Errorf("备份包含空或歧义实例 ID")
		}
		sourceProfiles[profile.id] = true
		normalizedProfiles[key] = true
	}
	sourceGroups := map[string]bool{}
	if hasGroups, err := backupSrcTableExists(tx, "browser_groups"); err != nil {
		return err
	} else if hasGroups {
		catalog, err := backupQueryImportGroups(tx, "src.browser_groups")
		if err != nil {
			return err
		}
		for _, group := range catalog {
			sourceGroups[group.ID] = true
		}
	}
	bySource := map[string][]string{}
	if canonical {
		rows, err := tx.Query(`SELECT profile_id,group_id FROM src.browser_profile_groups`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p, g string
			if err := rows.Scan(&p, &g); err != nil {
				rows.Close()
				return err
			}
			mapped := backupImportMappedID(mappings.Groups, g)
			if !sourceProfiles[p] || !sourceGroups[g] || mapped == "" || backupImportMappedID(mappings.Profiles, p) == "" {
				rows.Close()
				return fmt.Errorf("备份存在缺失的实例分组引用，导入已取消")
			}
			bySource[backupImportIDKey(p)] = append(bySource[backupImportIDKey(p)], mapped)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	for _, p := range profiles {
		id := backupImportMappedID(mappings.Profiles, p.id)
		if id == "" {
			return fmt.Errorf("备份实例缺少映射，无法恢复分组归属")
		}
		current, err := tx.Query(`SELECT group_id FROM browser_profile_groups WHERE profile_id=?`, id)
		if err != nil {
			return err
		}
		ids := []string{}
		for current.Next() {
			var g string
			if err := current.Scan(&g); err != nil {
				current.Close()
				return err
			}
			ids = append(ids, g)
		}
		if err := current.Err(); err != nil {
			current.Close()
			return err
		}
		current.Close()
		if canonical {
			ids = append(ids, bySource[backupImportIDKey(p.id)]...)
		} else {
			if p.group != "" {
				mapped := backupImportMappedID(mappings.Groups, p.group)
				if !sourceGroups[p.group] || mapped == "" {
					return fmt.Errorf("旧备份缺少实例分组引用，无法无损导入")
				}
				ids = append(ids, mapped)
			}
			var names []string
			if err := json.Unmarshal([]byte(p.tags), &names); err != nil {
				return fmt.Errorf("旧备份标签格式无效，导入已取消")
			}
			legacy, err := browser.ResolveLegacyRootGroupNames(tx, names)
			if err != nil {
				return err
			}
			ids = append(ids, legacy...)
		}
		profile := browser.Profile{ProfileId: id}
		if err := tx.QueryRow(`SELECT COALESCE(group_id,'') FROM browser_profiles WHERE profile_id=?`, id).Scan(&profile.GroupId); err != nil {
			return err
		}
		if err := browser.WriteProfileGroupMemberships(tx, &profile, ids); err != nil {
			return err
		}
	}
	return nil
}

// Validate the source independently of target ID mappings: an incomplete or
// ambiguous catalog must never borrow a coincidentally matching local record.
func validateImportedGroupCatalog(groups []backupImportGroupRecord, origin string) error {
	ids := map[string]bool{}
	siblings := map[string]bool{}
	parents := map[string]string{}
	exactIDs := map[string]bool{}
	for _, group := range groups {
		id := backupImportIDKey(group.ID)
		if id == "" || ids[id] {
			return fmt.Errorf("%s包含空或歧义分组 ID", origin)
		}
		if strings.TrimSpace(group.Name) == "" {
			return fmt.Errorf("%s包含空分组名称", origin)
		}
		key := backupImportIDKey(group.ParentID) + "\x00" + group.Name
		if siblings[key] {
			return fmt.Errorf("%s存在同级重名分组，无法无损导入", origin)
		}
		ids[id] = true
		siblings[key] = true
		parents[id] = backupImportIDKey(group.ParentID)
		exactIDs[group.ID] = true
	}
	for _, group := range groups {
		if group.ParentID != "" && !exactIDs[group.ParentID] {
			return fmt.Errorf("%s缺少精确匹配的父分组", origin)
		}
	}
	for id, parent := range parents {
		if parent != "" && !ids[parent] {
			return fmt.Errorf("%s缺少父分组", origin)
		}
		seen := map[string]bool{}
		for current := id; current != ""; current = parents[current] {
			if seen[current] {
				return fmt.Errorf("%s分组层级存在循环", origin)
			}
			seen[current] = true
		}
	}
	return nil
}
