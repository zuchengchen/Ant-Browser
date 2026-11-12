package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxArchiveLinkTarget = 4096

func prepareArchiveDestination(dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return ensureArchiveDirectory(dest, dest, 0o755)
}

// Extraction uses private temporary destinations. Refuse links in every path
// component so archive entries and pre-existing links cannot redirect writes.
func ensureArchiveDirectory(dest, dir string, mode os.FileMode) error {
	rel, err := filepath.Rel(dest, dir)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("非法解压目录: %s", dir)
	}
	current := filepath.Clean(dest)
	parts := []string{"."}
	if rel != "." {
		parts = append(parts, strings.Split(rel, string(os.PathSeparator))...)
	}
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, mode); err != nil {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("解压路径不是普通目录: %s", current)
		}
	}
	return nil
}

func createArchiveSymlinks(dest string, links map[string]string) error {
	// Validate the complete link graph before materializing it, including
	// forward references. Targets are interpreted in the final stripped layout.
	names := make([]string, 0, len(links))
	for name, target := range links {
		if err := validateArchiveSymlinkTarget(dest, name, target, links); err != nil {
			return err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		targetPath, err := safeArchiveTargetPath(dest, name)
		if err != nil {
			return err
		}
		if err := ensureArchiveDirectory(dest, filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Symlink(links[name], targetPath); err != nil {
			return fmt.Errorf("创建符号链接失败 %s: %w", name, err)
		}
	}
	return nil
}

func validateArchiveSymlinkTarget(dest, name, target string, links map[string]string) error {
	invalid := func() error {
		return fmt.Errorf("非法符号链接目标 %s -> %s", name, target)
	}
	validTarget := func(value string) bool {
		return value != "" && len(value) <= maxArchiveLinkTarget && !strings.ContainsRune(value, '\x00') && !filepath.IsAbs(value) && filepath.VolumeName(value) == ""
	}
	if !validTarget(target) {
		return invalid()
	}
	// Resolve components before cleaning '..': a link can change its meaning.
	// Missing targets inside dest are allowed (valid dangling relative links).
	pending := strings.Split(filepath.Dir(name)+string(os.PathSeparator)+target, string(os.PathSeparator))
	resolved := make([]string, 0, len(pending))
	hops := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return invalid()
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		resolved = append(resolved, part)
		rel := filepath.Join(resolved...)
		link, isLink := links[rel]
		if !isLink {
			path := filepath.Join(dest, rel)
			info, err := os.Lstat(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				link, err = os.Readlink(path)
				if err != nil {
					return err
				}
				isLink = true
			} else if err == nil && !info.IsDir() && len(pending) > 0 {
				return invalid()
			}
		}
		if isLink {
			hops++
			if hops > 255 || !validTarget(link) {
				return invalid()
			}
			resolved = resolved[:len(resolved)-1]
			pending = append(strings.Split(link, string(os.PathSeparator)), pending...)
		}
	}
	return nil
}
