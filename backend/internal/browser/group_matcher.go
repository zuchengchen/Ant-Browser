package browser

import (
	"fmt"
	"strings"
)

// InstanceGroupMatcher is shared by launch/runtime and automation selectors.
// Multiple legacy name conditions keep their historical AND semantics.
func (m *Manager) InstanceGroupMatcher(groupID string, names []string) (func(Profile) bool, error) {
	if groupID == "" && len(names) == 0 {
		return func(Profile) bool { return true }, nil
	}
	if m.GroupDAO == nil {
		// File-backed configurations and old in-memory callers have no catalog.
		return func(p Profile) bool {
			ids := p.GroupIds
			if ids == nil && p.GroupId != "" {
				ids = []string{p.GroupId}
			}
			if groupID != "" {
				found := false
				for _, id := range ids {
					if id == groupID {
						found = true
					}
				}
				if !found {
					return false
				}
			}
			for _, want := range names {
				found := false
				for _, name := range p.Tags {
					if strings.EqualFold(strings.TrimSpace(name), want) {
						found = true
					}
				}
				if !found {
					return false
				}
			}
			return true
		}, nil
	}
	groups, err := m.GroupDAO.List()
	if err != nil {
		return nil, err
	}
	byID := map[string]*Group{}
	children := map[string][]string{}
	for _, g := range groups {
		byID[g.GroupId] = g
		children[g.ParentId] = append(children[g.ParentId], g.GroupId)
	}
	roots := []string{}
	if groupID != "" {
		if byID[groupID] == nil {
			return nil, fmt.Errorf("分组不存在，请刷新后重试")
		}
		roots = append(roots, groupID)
	}
	for _, name := range names {
		id := ""
		for _, g := range groups {
			if strings.EqualFold(strings.TrimSpace(g.GroupName), strings.TrimSpace(name)) {
				if id != "" {
					return nil, fmt.Errorf("分组名称有歧义，请使用 groupId 指定分组")
				}
				id = g.GroupId
			}
		}
		if id == "" {
			return nil, fmt.Errorf("分组名称不存在，请刷新后重试")
		}
		roots = append(roots, id)
	}
	sets := []map[string]bool{}
	for _, root := range roots {
		set := map[string]bool{}
		queue := []string{root}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if set[id] {
				continue
			}
			set[id] = true
			queue = append(queue, children[id]...)
		}
		sets = append(sets, set)
	}
	return func(p Profile) bool {
		ids := p.GroupIds
		if ids == nil && p.GroupId != "" {
			ids = []string{p.GroupId}
		}
		for _, set := range sets {
			found := false
			for _, id := range ids {
				if set[id] {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}, nil
}
