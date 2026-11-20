package browser

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type GroupDAO interface {
	List() ([]*Group, error)
	GetById(string) (*Group, error)
	Create(GroupInput) (*Group, error)
	Update(string, GroupInput) (*Group, error)
	Delete(string) error
	GetChildren(string) ([]*Group, error)
	MoveChildren(string, string) error
}
type SQLiteGroupDAO struct{ db *sql.DB }

func NewSQLiteGroupDAO(db *sql.DB) *SQLiteGroupDAO { return &SQLiteGroupDAO{db: db} }

const groupSelect = `SELECT group_id,group_name,COALESCE(parent_id,''),sort_order,created_at,updated_at FROM browser_groups`

func (d *SQLiteGroupDAO) List() ([]*Group, error) {
	rows, err := d.db.Query(groupSelect + ` ORDER BY sort_order,created_at,group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []*Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
func (d *SQLiteGroupDAO) GetById(id string) (*Group, error) { return getGroup(d.db, id) }

type groupQuerier interface{ QueryRow(string, ...any) *sql.Row }

func getGroup(q groupQuerier, id string) (*Group, error) {
	g, err := scanGroup(q.QueryRow(groupSelect+` WHERE group_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("分组不存在，请刷新后重试")
	}
	return g, err
}
func validateGroup(q groupQuerier, id string, input GroupInput) error {
	if strings.TrimSpace(input.GroupName) == "" {
		return errors.New("分组名称不能为空")
	}
	visited := map[string]bool{}
	for current := input.ParentId; current != ""; {
		if current == id || visited[current] {
			return errors.New("分组不能移动到自己或自己的子分组中")
		}
		visited[current] = true
		parent, err := getGroup(q, current)
		if err != nil {
			return fmt.Errorf("父分组不存在或层级无效: %w", err)
		}
		current = parent.ParentId
	}
	var count int
	if err := q.QueryRow(`SELECT COUNT(*) FROM browser_groups WHERE COALESCE(parent_id,'')=? AND group_name=? AND group_id!=?`, input.ParentId, input.GroupName, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("同级分组名称已存在，请使用其他名称")
	}
	return nil
}
func (d *SQLiteGroupDAO) Create(input GroupInput) (*Group, error) {
	input.GroupName = strings.TrimSpace(input.GroupName)
	input.ParentId = strings.TrimSpace(input.ParentId)
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id := uuid.NewString()
	if err := validateGroup(tx, id, input); err != nil {
		return nil, err
	}
	now := time.Now().Format(time.RFC3339Nano)
	g := &Group{GroupId: id, GroupName: input.GroupName, ParentId: input.ParentId, SortOrder: input.SortOrder, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.Exec(`INSERT INTO browser_groups(group_id,group_name,parent_id,sort_order,created_at,updated_at) VALUES(?,?,?,?,?,?)`, g.GroupId, g.GroupName, g.ParentId, g.SortOrder, g.CreatedAt, g.UpdatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return g, nil
}
func (d *SQLiteGroupDAO) Update(id string, input GroupInput) (*Group, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	g, err := updateGroupTx(tx, id, input)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return g, nil
}
func updateGroupTx(tx *sql.Tx, id string, input GroupInput) (*Group, error) {
	input.GroupName = strings.TrimSpace(input.GroupName)
	input.ParentId = strings.TrimSpace(input.ParentId)
	g, err := getGroup(tx, id)
	if err != nil {
		return nil, err
	}
	if err := validateGroup(tx, id, input); err != nil {
		return nil, err
	}
	g.GroupName = input.GroupName
	g.ParentId = input.ParentId
	g.SortOrder = input.SortOrder
	g.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`UPDATE browser_groups SET group_name=?,parent_id=?,sort_order=?,updated_at=? WHERE group_id=?`, g.GroupName, g.ParentId, g.SortOrder, g.UpdatedAt, id); err != nil {
		return nil, err
	}
	if err := refreshLegacyGroupColumns(tx); err != nil {
		return nil, err
	}
	return g, nil
}
func (d *SQLiteGroupDAO) Delete(id string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteGroupTx(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}
func deleteGroupTx(tx *sql.Tx, id string) error {
	g, err := getGroup(tx, id)
	if err != nil {
		return err
	}
	var conflicts int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM browser_groups child JOIN browser_groups sibling ON sibling.group_name=child.group_name
 WHERE child.parent_id=? AND COALESCE(sibling.parent_id,'')=? AND sibling.group_id!=?`, id, g.ParentId, id).Scan(&conflicts); err != nil {
		return err
	}
	if conflicts > 0 {
		return errors.New("子分组提升后会出现同级重名，请先重命名冲突分组")
	}
	now := time.Now().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`UPDATE browser_profiles SET updated_at=? WHERE profile_id IN (SELECT profile_id FROM browser_profile_groups WHERE group_id=?)`, now, id); err != nil {
		return err
	}
	// Delete before promotion: a child may legitimately have its parent's name.
	if _, err := tx.Exec(`DELETE FROM browser_profile_groups WHERE group_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM browser_groups WHERE group_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE browser_groups SET parent_id=?,updated_at=? WHERE parent_id=?`, g.ParentId, now, id); err != nil {
		return err
	}
	return refreshLegacyGroupColumns(tx)
}
func (d *SQLiteGroupDAO) GetChildren(parent string) ([]*Group, error) {
	groups, err := d.List()
	if err != nil {
		return nil, err
	}
	children := []*Group{}
	for _, g := range groups {
		if g.ParentId == parent {
			children = append(children, g)
		}
	}
	return children, nil
}
func (d *SQLiteGroupDAO) MoveChildren(from, to string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(groupSelect+` WHERE parent_id=?`, from)
	if err != nil {
		return err
	}
	children := []*Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			rows.Close()
			return err
		}
		children = append(children, g)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, g := range children {
		if err := validateGroup(tx, g.GroupId, GroupInput{GroupName: g.GroupName, ParentId: to}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE browser_groups SET parent_id=? WHERE parent_id=?`, to, from); err != nil {
		return err
	}
	return tx.Commit()
}
func scanGroup(s scanner) (*Group, error) {
	var g Group
	err := s.Scan(&g.GroupId, &g.GroupName, &g.ParentId, &g.SortOrder, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &g, nil
}
