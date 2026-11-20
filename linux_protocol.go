package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Portable Linux installations can register the same native binary with the
// user's desktop. The Debian package installs its system desktop entry itself.
func registerLinuxProtocol() error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(dataDir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	entry, err := portableDesktopEntry(path)
	if err != nil {
		return err
	}
	const desktopID = "ant-browser-portable.desktop"
	dir := filepath.Join(dataDir, "applications")
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".ant-browser-*.desktop")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(entry)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, desktopID)); err != nil {
		return err
	}
	if cmd, err := exec.LookPath("update-desktop-database"); err == nil {
		if err = exec.Command(cmd, dir).Run(); err != nil {
			return fmt.Errorf("更新桌面应用列表失败: %w", err)
		}
	}
	if err = exec.Command("xdg-mime", "default", desktopID, "x-scheme-handler/ant-browser").Run(); err != nil {
		return fmt.Errorf("注册 ant-browser 链接失败: %w", err)
	}
	return nil
}

func portableDesktopEntry(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return "", errors.New("便携版需要有效的绝对路径")
	}
	// Quote the executable as one Exec argument, then apply desktop entry string
	// escaping. Literal percent signs must not be interpreted as field codes.
	quoted := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$", "`", "\\`", "%", "%%").Replace(path)
	quoted = strings.ReplaceAll(quoted, "\\", "\\\\")
	return fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Ant Browser\nExec=\"%s\" %%u\nIcon=web-browser\nTerminal=false\nCategories=Network;\nMimeType=x-scheme-handler/ant-browser;\n", quoted), nil
}
