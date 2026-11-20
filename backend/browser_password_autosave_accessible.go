package backend

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
)

type browserPasswordAccessible struct{ Bus, Path string }
type browserPasswordAccessibleReader interface {
	Role(browserPasswordAccessible) (uint32, error)
	Name(browserPasswordAccessible) (string, error)
	State(browserPasswordAccessible) ([]uint32, error)
	Children(browserPasswordAccessible) ([]browserPasswordAccessible, error)
	Actions(browserPasswordAccessible) ([]string, error)
}
type browserPasswordAccessibleButton struct {
	Ref, Dialog  browserPasswordAccessible
	Title, Label string
	Action       int32
}

func browserPasswordDialogLabels(title string) []string {
	switch title {
	case "Save password?", "Save Password?":
		return []string{"Save"}
	case "Update password?", "Update Password?":
		return []string{"Update password", "Update Password", "Update"}
	case "要保存密码吗？":
		return []string{"保存"}
	case "要更新密码吗？":
		return []string{"更新密码", "更新"}
	case "要儲存密碼嗎？":
		return []string{"儲存"}
	case "要更新密碼嗎？":
		return []string{"更新密碼", "更新"}
	}
	for _, pattern := range []struct {
		prefix, suffix string
		labels         []string
	}{
		{"Save password for ", "?", []string{"Save"}}, {"Update password for ", "?", []string{"Update password", "Update Password", "Update"}},
		{"要保存 ", " 的密码吗？", []string{"保存"}}, {"要更新 ", " 的密码吗？", []string{"更新密码", "更新"}},
		{"要儲存 ", " 的密碼嗎？", []string{"儲存"}}, {"要更新 ", " 的密碼嗎？", []string{"更新密碼", "更新"}},
	} {
		if strings.HasPrefix(title, pattern.prefix) && strings.HasSuffix(title, pattern.suffix) {
			origin := strings.TrimSuffix(strings.TrimPrefix(title, pattern.prefix), pattern.suffix)
			if browserPasswordNativeOrigin(origin) {
				return pattern.labels
			}
		}
	}
	return nil
}

func browserPasswordNativeOrigin(origin string) bool {
	if origin == "" || len(origin) > 512 || strings.ContainsAny(origin, "\r\n\t /\\<>@?#") || strings.ContainsFunc(origin, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	parsed, err := url.Parse("https://" + origin)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Host != origin {
		return false
	}
	host := parsed.Hostname()
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 || strings.ContainsAny(host, "[]:") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if r != '-' && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 127) {
				return false
			}
		}
	}
	return true
}

func browserPasswordAXState(states []uint32, index uint) bool {
	return int(index/32) < len(states) && states[index/32]&(uint32(1)<<(index%32)) != 0
}
func browserPasswordAXVisible(states []uint32) bool {
	return browserPasswordAXState(states, 25) && browserPasswordAXState(states, 30) && !browserPasswordAXState(states, 6)
}
func browserPasswordAXPruned(role uint32) bool {
	switch role {
	case 0, 40, 60, 61, 78, 79, 82, 92, 93, 94, 95, 96:
		return true
	}
	return false
}

// Only native dialog/button names are read. Document, embedded, password and
// text subtrees are never entered; password controls are identified by role.
func inspectBrowserPasswordAccessible(reader browserPasswordAccessibleReader, root browserPasswordAccessible) ([]browserPasswordAccessibleButton, error) {
	type entry struct {
		ref, dialog browserPasswordAccessible
		title       string
		labels      []string
		depth       int
	}
	queue := []entry{{ref: root}}
	seen := make(map[browserPasswordAccessible]bool)
	byDialog := make(map[browserPasswordAccessible][]browserPasswordAccessibleButton)
	passwords := make(map[browserPasswordAccessible]bool)
	for len(queue) > 0 {
		item := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[item.ref] || item.ref.Bus != root.Bus || item.ref.Path == "" || item.ref.Path == "/org/a11y/atspi/null" {
			continue
		}
		if len(seen) >= 4000 || item.depth > 60 {
			return nil, fmt.Errorf("native accessibility tree limit exceeded")
		}
		seen[item.ref] = true
		role, err := reader.Role(item.ref)
		if err != nil {
			return nil, err
		}
		if role == 40 && item.dialog.Path != "" {
			passwords[item.dialog] = true
		}
		if browserPasswordAXPruned(role) {
			continue
		}
		if role == 2 || role == 16 {
			title, err := reader.Name(item.ref)
			if err != nil {
				return nil, err
			}
			labels := browserPasswordDialogLabels(title)
			if len(labels) == 0 {
				continue
			}
			states, err := reader.State(item.ref)
			if err != nil {
				return nil, err
			}
			if !browserPasswordAXVisible(states) {
				continue
			}
			item.dialog, item.title, item.labels = item.ref, title, labels
		}
		if role == 43 && item.dialog.Path != "" {
			label, err := reader.Name(item.ref)
			if err != nil {
				return nil, err
			}
			accepted := false
			for _, expected := range item.labels {
				if label == expected {
					accepted = true
				}
			}
			if !accepted {
				continue
			}
			states, err := reader.State(item.ref)
			if err != nil {
				return nil, err
			}
			if !browserPasswordAXVisible(states) || !browserPasswordAXState(states, 8) || !browserPasswordAXState(states, 24) {
				continue
			}
			actions, err := reader.Actions(item.ref)
			if err != nil {
				return nil, err
			}
			if len(actions) > 16 {
				return nil, fmt.Errorf("native action limit exceeded")
			}
			for index, action := range actions {
				if action == "press" {
					byDialog[item.dialog] = append(byDialog[item.dialog], browserPasswordAccessibleButton{Ref: item.ref, Dialog: item.dialog, Title: item.title, Label: label, Action: int32(index)})
					break
				}
			}
			continue
		}
		children, err := reader.Children(item.ref)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			queue = append(queue, entry{ref: child, dialog: item.dialog, title: item.title, labels: item.labels, depth: item.depth + 1})
		}
	}
	var buttons []browserPasswordAccessibleButton
	for dialog, candidates := range byDialog {
		if len(candidates) == 1 && passwords[dialog] {
			buttons = append(buttons, candidates[0])
		}
	}
	return buttons, nil
}
