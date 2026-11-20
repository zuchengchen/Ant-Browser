package backend

import (
	"ant-chrome/backend/internal/browser"
	"fmt"
)

// Legacy name inputs are an adapter to the canonical membership relation.
func (a *App) BrowserProfileBatchSetTags(profileIDs, tags []string, replace bool) error {
	if replace {
		return fmt.Errorf("旧标签替换可能丢失多个分组归属，请使用 BrowserProfileChangeGroups 的 set 模式")
	}
	return a.changeLegacyProfileTags(profileIDs, tags, false)
}
func (a *App) BrowserProfileBatchRemoveTags(profileIDs, tags []string) error {
	return a.changeLegacyProfileTags(profileIDs, tags, true)
}
func (a *App) changeLegacyProfileTags(profileIDs, tags []string, remove bool) error {
	m, err := a.instanceGroupManager()
	if err != nil {
		return err
	}
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	dao, ok := m.ProfileDAO.(*browser.SQLiteProfileDAO)
	if !ok {
		return fmt.Errorf("当前存储不支持实例分组")
	}
	snapshot, err := dao.ChangeLegacyTagsWithSnapshot(profileIDs, tags, remove)
	if err != nil {
		return err
	}
	m.ApplyGroupSnapshotLocked(snapshot)
	return nil
}
func (a *App) BrowserRenameTag(oldName, newName string) error {
	m, err := a.instanceGroupManager()
	if err != nil {
		return err
	}
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	groups, err := m.GroupDAO.List()
	if err != nil {
		return err
	}
	var target *browser.Group
	for _, g := range groups {
		if g.GroupName == oldName {
			if target != nil {
				return fmt.Errorf("分组名称有歧义，请使用分组 ID 重命名")
			}
			target = g
		}
	}
	if target == nil {
		return fmt.Errorf("分组不存在，请刷新后重试")
	}
	dao, ok := m.ProfileDAO.(*browser.SQLiteProfileDAO)
	if !ok {
		return fmt.Errorf("当前存储不支持实例分组")
	}
	input := browser.GroupInput{GroupName: newName, ParentId: target.ParentId, SortOrder: target.SortOrder}
	_, snapshot, err := dao.ChangeGroupDefinition(target.GroupId, &input)
	if err != nil {
		return err
	}
	m.ApplyGroupSnapshotLocked(snapshot)
	return nil
}
