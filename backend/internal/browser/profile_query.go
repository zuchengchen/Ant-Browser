package browser

import (
	"ant-chrome/backend/internal/logger"
	"sort"
	"strings"
)

// List 获取配置列表
func (m *Manager) List() []Profile {
	log := logger.New("Browser")
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	list := make([]Profile, 0, len(m.Profiles))
	for _, profile := range m.Profiles {
		p := *profile
		if m.CodeProvider != nil {
			if code, err := m.CodeProvider.EnsureCode(p.ProfileId); err == nil {
				p.LaunchCode = code
			}
		}
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ProfileId < list[j].ProfileId
	})
	log.Debug("浏览器配置列表查询", logger.F("count", len(list)))
	return list
}

// ListByGroupName adapts legacy tag selectors to unambiguous instance groups.
func (m *Manager) ListByGroupName(tag string) ([]Profile, error) {
	all := m.List()
	names := []string{}
	if strings.TrimSpace(tag) != "" {
		names = append(names, strings.TrimSpace(tag))
	}
	matches, err := m.InstanceGroupMatcher("", names)
	if err != nil {
		return nil, err
	}
	result := make([]Profile, 0)
	for _, p := range all {
		if matches(p) {
			result = append(result, p)
		}
	}
	return result, nil
}

// ListByTag is retained for internal legacy callers. Public APIs use the error-aware adapter.
func (m *Manager) ListByTag(tag string) []Profile {
	result, err := m.ListByGroupName(tag)
	if err != nil {
		logger.New("Browser").Error("分组名称筛选失败", logger.F("error", err))
		return []Profile{}
	}
	return result
}

// GetAllTags 获取所有已使用的标签（去重排序）
func (m *Manager) GetAllTags() []string {
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	seen := make(map[string]struct{})
	for _, p := range m.Profiles {
		for _, t := range p.Tags {
			t = strings.TrimSpace(t)
			if t != "" {
				seen[t] = struct{}{}
			}
		}
	}
	tags := make([]string, 0, len(seen))
	for t := range seen {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	return tags
}
