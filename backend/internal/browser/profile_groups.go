package browser

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAmbiguousGroupName   = errors.New("分组名称有歧义，请改用 groupIds 指定分组")
	ErrRootGroupUnavailable = errors.New("目标顶层分组不可用")
)

func CloneGroupIDs(ids []string) []string {
	if ids == nil {
		return nil
	}
	return append([]string{}, ids...)
}

func uniqueGroupIDs(ids []string) []string {
	set := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = true
		}
	}
	result := make([]string, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func readProfileGroupIDs(tx *sql.Tx, profileID string) ([]string, error) {
	rows, err := tx.Query(`SELECT group_id FROM browser_profile_groups WHERE profile_id=? ORDER BY group_id`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ResolveLegacyGroupNames rejects ambiguous names across branches. Creation is
// reserved for legacy writes/imports and creates a persistent root group.
func ResolveLegacyGroupNames(tx *sql.Tx, names []string, create bool) ([]string, error) {
	return resolveLegacyGroupNames(tx, names, create, false)
}
func ResolveLegacyRootGroupNames(tx *sql.Tx, names []string) ([]string, error) {
	return resolveLegacyGroupNames(tx, names, true, true)
}

func ensureRootGroup(tx *sql.Tx, name string) (string, error) {
	ids, err := ResolveLegacyRootGroupNames(tx, []string{strings.TrimSpace(name)})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRootGroupUnavailable, err)
	}
	if len(ids) != 1 {
		return "", ErrRootGroupUnavailable
	}
	return ids[0], nil
}

func resolveLegacyGroupNames(tx *sql.Tx, names []string, create, rootOnly bool) ([]string, error) {
	ids := []string{}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("分组名称不能为空")
		}
		query := `SELECT group_id FROM browser_groups WHERE group_name=?`
		if rootOnly {
			query += ` AND COALESCE(parent_id,'')=''`
		}
		rows, err := tx.Query(query, name)
		if err != nil {
			return nil, err
		}
		matches := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			matches = append(matches, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if len(matches) > 1 {
			return nil, ErrAmbiguousGroupName
		}
		if len(matches) == 1 {
			ids = append(ids, matches[0])
			continue
		}
		if !create {
			return nil, fmt.Errorf("分组名称不存在，请刷新分组列表")
		}
		id := uuid.NewString()
		if _, err := tx.Exec(`INSERT INTO browser_groups (group_id,group_name,parent_id) VALUES (?,?,'')`, id, name); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return uniqueGroupIDs(ids), nil
}

func resolveProfileGroupInput(tx *sql.Tx, p *Profile) ([]string, error) {
	if p.GroupIds != nil {
		if err := validateGroupIDs(p.GroupIds); err != nil {
			return nil, err
		}
		return uniqueGroupIDs(p.GroupIds), nil
	}
	ids, err := readProfileGroupIDs(tx, p.ProfileId)
	if err != nil {
		return nil, err
	}
	if p.GroupId != "" {
		ids = append(ids, p.GroupId)
	}
	legacy, err := ResolveLegacyGroupNames(tx, p.Tags, true)
	if err != nil {
		return nil, err
	}
	return uniqueGroupIDs(append(ids, legacy...)), nil
}

// PersistProfileGroups is shared by SQL imports and the normal DAO. It does not
// commit: the caller owns the surrounding profile/import transaction.
func PersistProfileGroups(tx *sql.Tx, p *Profile) error {
	ids, err := resolveProfileGroupInput(tx, p)
	if err != nil {
		return err
	}
	return WriteProfileGroupMemberships(tx, p, ids)
}

func WriteProfileGroupMemberships(tx *sql.Tx, p *Profile, ids []string) error {
	if err := validateGroupIDs(ids); err != nil {
		return err
	}
	ids = uniqueGroupIDs(ids)
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		var name string
		if err := tx.QueryRow(`SELECT group_name FROM browser_groups WHERE group_id=?`, id).Scan(&name); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("分组不存在，请刷新后重试")
			}
			return err
		}
		names = append(names, name)
	}
	if _, err := tx.Exec(`DELETE FROM browser_profile_groups WHERE profile_id=?`, p.ProfileId); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO browser_profile_groups (profile_id,group_id) VALUES (?,?)`, p.ProfileId, id); err != nil {
			return err
		}
	}
	primary := ""
	if len(ids) > 0 {
		primary = ids[0]
		for _, id := range ids {
			if id == p.GroupId {
				primary = id
			}
		}
	}
	tags, _ := json.Marshal(names)
	if _, err := tx.Exec(`UPDATE browser_profiles SET group_id=?,tags=? WHERE profile_id=? AND (COALESCE(group_id,'')!=? OR COALESCE(tags,'[]')!=?)`, primary, string(tags), p.ProfileId, primary, string(tags)); err != nil {
		return err
	}
	p.GroupIds = ids
	p.GroupId = primary
	p.Tags = names
	return nil
}

func (d *SQLiteProfileDAO) changeGroupsTx(tx *sql.Tx, profileIDs, groupIDs []string, mode string) error {
	if mode != "add" && mode != "remove" && mode != "set" {
		return fmt.Errorf("无效的分组操作")
	}
	if err := validateGroupIDs(groupIDs); err != nil {
		return err
	}
	if err := validateGroupIDs(profileIDs); err != nil {
		return err
	}
	groupIDs = uniqueGroupIDs(groupIDs)
	for _, id := range groupIDs {
		var found string
		if err := tx.QueryRow(`SELECT group_id FROM browser_groups WHERE group_id=?`, id).Scan(&found); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("分组不存在，请刷新后重试")
			}
			return err
		}
	}
	for _, id := range uniqueGroupIDs(profileIDs) {
		var p Profile
		p.ProfileId = id
		if err := tx.QueryRow(`SELECT COALESCE(group_id,'') FROM browser_profiles WHERE profile_id=? AND COALESCE(deleted_at,'')=''`, id).Scan(&p.GroupId); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("实例不存在或已在回收站，批量操作未执行")
			}
			return err
		}
		ids, err := readProfileGroupIDs(tx, id)
		if err != nil {
			return err
		}
		switch mode {
		case "add":
			ids = append(ids, groupIDs...)
		case "set":
			ids = groupIDs
		case "remove":
			remove := map[string]bool{}
			for _, g := range groupIDs {
				remove[g] = true
			}
			kept := []string{}
			for _, g := range ids {
				if !remove[g] {
					kept = append(kept, g)
				}
			}
			ids = kept
		}
		if err := WriteProfileGroupMemberships(tx, &p, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE browser_profiles SET updated_at=? WHERE profile_id=?`, time.Now().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
	}
	return nil
}

func (d *SQLiteProfileDAO) ChangeGroups(profileIDs, groupIDs []string, mode string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := d.changeGroupsTx(tx, profileIDs, groupIDs, mode); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *SQLiteProfileDAO) ChangeLegacyTags(profileIDs, names []string, remove bool) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids, err := ResolveLegacyGroupNames(tx, names, !remove)
	if err != nil {
		return err
	}
	mode := "add"
	if remove {
		mode = "remove"
	}
	if err := d.changeGroupsTx(tx, profileIDs, ids, mode); err != nil {
		return err
	}
	return tx.Commit()
}

// Caller holds Manager.Mutex. Keep process/runtime state while replacing only
// persisted classification projections, including after rename or deletion.
func (m *Manager) RefreshGroupMembershipsLocked() error {
	profiles, err := m.ProfileDAO.List()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if current := m.Profiles[p.ProfileId]; current != nil {
			current.GroupIds = CloneGroupIDs(p.GroupIds)
			current.GroupId = p.GroupId
			current.Tags = append([]string{}, p.Tags...)
			current.UpdatedAt = p.UpdatedAt
		}
	}
	return nil
}

func validateGroupIDs(ids []string) error {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("分组或实例 ID 不能为空；清空归属请明确传入空数组")
		}
	}
	return nil
}

// Compatibility columns are materialized projections, never a read authority.
func refreshLegacyGroupColumns(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE browser_profiles SET tags=(SELECT json_group_array(group_name) FROM (
 SELECT g.group_name FROM browser_profile_groups m JOIN browser_groups g ON g.group_id=m.group_id
 WHERE m.profile_id=browser_profiles.profile_id ORDER BY m.group_id)),
 group_id=COALESCE((SELECT m.group_id FROM browser_profile_groups m WHERE m.profile_id=browser_profiles.profile_id AND m.group_id=browser_profiles.group_id),
 (SELECT MIN(m.group_id) FROM browser_profile_groups m WHERE m.profile_id=browser_profiles.profile_id),'')`)
	return err
}

func readGroupSnapshotTx(tx *sql.Tx) ([]*Profile, error) {
	rows, err := tx.Query(profileSelect + " WHERE COALESCE(deleted_at,'')=''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (d *SQLiteProfileDAO) ChangeGroupsWithSnapshot(profileIDs, groupIDs []string, mode string) ([]*Profile, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := d.changeGroupsTx(tx, profileIDs, groupIDs, mode); err != nil {
		return nil, err
	}
	snapshot, err := readGroupSnapshotTx(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// EnsureProfileRootGroup resolves the named root and adds its membership in
// one transaction. Return the committed profile so the manager can refresh its
// classification while retaining process state.
func (d *SQLiteProfileDAO) EnsureProfileRootGroup(profileID, name string) (*Profile, bool, error) {
	return d.UpdateBoundProfile(profileID, name, nil)
}

// BoundProfileRename records the observed name for a compare-and-swap against
// the active row. BeforeCommit can retire the old account after a successful write.
type BoundProfileRename struct {
	From, To     string
	BeforeCommit func() error
}

func renameBoundProfileTx(tx *sql.Tx, profileID string, rename *BoundProfileRename) error {
	rows, err := tx.Query(`SELECT profile_name FROM browser_profiles WHERE profile_id<>? AND COALESCE(deleted_at,'')=''`, profileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		// SQLite NOCASE only folds ASCII. Keep the same matching rules as
		// the resolver and the creation of a new bound copy.
		if strings.EqualFold(name, rename.To) {
			return ErrBoundProfileConflict
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE browser_profiles SET profile_name=?,updated_at=?
		WHERE profile_id=? AND profile_name=? AND COALESCE(deleted_at,'')=''`,
		rename.To, time.Now().Format(time.RFC3339Nano), profileID, rename.From)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrBoundProfileConflict
	}
	return err
}

// UpdateBoundProfile changes only the bound name and group membership. All
// SQL writes and snapshot reads must succeed before the caller retires the
// previous login. The caller can restore it if the final commit fails.
func (d *SQLiteProfileDAO) UpdateBoundProfile(profileID, groupName string, rename *BoundProfileRename) (*Profile, bool, error) {
	ctx := context.Background()
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if rename != nil {
		if err := renameBoundProfileTx(tx, profileID, rename); err != nil {
			return nil, false, err
		}
	}
	id, err := ensureRootGroup(tx, groupName)
	if err != nil {
		return nil, false, err
	}
	var member bool
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM browser_profile_groups WHERE profile_id=? AND group_id=?)`, profileID, id).Scan(&member)
	if err != nil {
		return nil, false, err
	}
	if !member {
		if err := d.changeGroupsTx(tx, []string{profileID}, []string{id}, "add"); err != nil {
			return nil, false, err
		}
	}
	p, err := scanProfile(tx.QueryRow(profileSelect+` WHERE profile_id=? AND COALESCE(deleted_at,'')=''`, profileID))
	if err != nil {
		return nil, false, err
	}
	if rename != nil && rename.BeforeCommit != nil {
		if err := rename.BeforeCommit(); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		// SQLite retains the transaction after some commit errors (e.g.
		// deferred foreign keys), but database/sql already marks tx done.
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return nil, false, err
	}
	return p, !member || rename != nil, nil
}

func (d *SQLiteProfileDAO) ChangeLegacyTagsWithSnapshot(profileIDs, names []string, remove bool) ([]*Profile, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids, err := ResolveLegacyGroupNames(tx, names, !remove)
	if err != nil {
		return nil, err
	}
	mode := "add"
	if remove {
		mode = "remove"
	}
	if err := d.changeGroupsTx(tx, profileIDs, ids, mode); err != nil {
		return nil, err
	}
	snapshot, err := readGroupSnapshotTx(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return snapshot, nil
}
func (d *SQLiteProfileDAO) ChangeGroupDefinition(id string, input *GroupInput) (*Group, []*Profile, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var group *Group
	if input == nil {
		err = deleteGroupTx(tx, id)
	} else {
		group, err = updateGroupTx(tx, id, *input)
	}
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := readGroupSnapshotTx(tx)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return group, snapshot, nil
}
func (m *Manager) ApplyGroupSnapshotLocked(snapshot []*Profile) {
	for _, p := range snapshot {
		if current := m.Profiles[p.ProfileId]; current != nil {
			current.GroupIds = CloneGroupIDs(p.GroupIds)
			current.GroupId = p.GroupId
			current.Tags = append([]string{}, p.Tags...)
			current.UpdatedAt = p.UpdatedAt
		}
	}
}

// Old packages kept tags separate from hierarchical groups, so migrate each tag
// to a root group, just as the versioned database migration does.
func PersistLegacyImportedProfileGroups(tx *sql.Tx, p *Profile) error {
	if p.GroupIds != nil {
		return PersistProfileGroups(tx, p)
	}
	ids, err := readProfileGroupIDs(tx, p.ProfileId)
	if err != nil {
		return err
	}
	legacy, err := ResolveLegacyRootGroupNames(tx, p.Tags)
	if err != nil {
		return err
	}
	ids = append(ids, legacy...)
	if p.GroupId != "" {
		ids = append(ids, p.GroupId)
	}
	return WriteProfileGroupMemberships(tx, p, ids)
}
