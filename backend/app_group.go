package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/logger"
	"fmt"
)

type BrowserGroup = browser.Group
type BrowserGroupInput = browser.GroupInput
type BrowserGroupWithCount = browser.GroupWithCount

func (a *App) instanceGroupManager() (*browser.Manager, error) {
	if a == nil || a.browserMgr == nil || a.browserMgr.GroupDAO == nil || a.browserMgr.ProfileDAO == nil {
		return nil, fmt.Errorf("实例分组尚未初始化，请等待启动完成")
	}
	return a.browserMgr, nil
}

// BrowserInstanceGroupList preserves a real error for the modern UI.
func (a *App) BrowserInstanceGroupList() ([]BrowserGroupWithCount, error) {
	m, err := a.instanceGroupManager()
	if err != nil {
		return nil, err
	}
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	groups, err := m.GroupDAO.List()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	if counter, ok := m.ProfileDAO.(browser.ProfileGroupCounter); ok {
		counts, err = counter.CountByGroup()
	} else {
		var profiles []*browser.Profile
		profiles, err = m.ProfileDAO.List()
		if err == nil {
			parents := map[string]string{}
			members := map[string]map[string]bool{}
			for _, g := range groups {
				parents[g.GroupId] = g.ParentId
			}
			for _, p := range profiles {
				ids := p.GroupIds
				if ids == nil && p.GroupId != "" {
					ids = []string{p.GroupId}
				}
				for _, id := range ids {
					seen := map[string]bool{}
					for current := id; current != "" && !seen[current]; current = parents[current] {
						seen[current] = true
						if members[current] == nil {
							members[current] = map[string]bool{}
						}
						members[current][p.ProfileId] = true
					}
				}
			}
			for id, set := range members {
				counts[id] = len(set)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	result := make([]BrowserGroupWithCount, 0, len(groups))
	for _, g := range groups {
		result = append(result, BrowserGroupWithCount{Group: *g, InstanceCount: counts[g.GroupId]})
	}
	return result, nil
}

// ListGroups remains the legacy read binding. New clients use the error-aware API.
func (a *App) ListGroups() []BrowserGroupWithCount {
	groups, err := a.BrowserInstanceGroupList()
	if err != nil {
		logger.New("Group").Error("获取实例分组失败", logger.F("error", err))
		return []BrowserGroupWithCount{}
	}
	return groups
}
func (a *App) CreateGroup(input BrowserGroupInput) (*BrowserGroup, error) {
	m, err := a.instanceGroupManager()
	if err != nil {
		return nil, err
	}
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	return m.GroupDAO.Create(input)
}
func (a *App) UpdateGroup(id string, input BrowserGroupInput) (*BrowserGroup, error) {
	m, err := a.instanceGroupManager()
	if err != nil {
		return nil, err
	}
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	dao, ok := m.ProfileDAO.(*browser.SQLiteProfileDAO)
	if !ok {
		return nil, fmt.Errorf("当前存储不支持实例分组")
	}
	group, snapshot, err := dao.ChangeGroupDefinition(id, &input)
	if err != nil {
		return nil, err
	}
	m.ApplyGroupSnapshotLocked(snapshot)
	return group, nil
}
func (a *App) DeleteGroup(id string) error {
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
	_, snapshot, err := dao.ChangeGroupDefinition(id, nil)
	if err != nil {
		return err
	}
	m.ApplyGroupSnapshotLocked(snapshot)
	return nil
}

// BrowserProfileChangeGroups applies one atomic batch. set is an explicit full
// replacement; add/remove leave every other membership unchanged.
func (a *App) BrowserProfileChangeGroups(profileIDs, groupIDs []string, mode string) error {
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
	snapshot, err := dao.ChangeGroupsWithSnapshot(profileIDs, groupIDs, mode)
	if err != nil {
		return err
	}
	m.ApplyGroupSnapshotLocked(snapshot)
	return nil
}
func (a *App) MoveInstancesToGroup(profileIDs []string, id string) error {
	if id == "" {
		return fmt.Errorf("清空分组请使用 BrowserProfileChangeGroups 的 set 模式")
	}
	return a.BrowserProfileChangeGroups(profileIDs, []string{id}, "add")
}
