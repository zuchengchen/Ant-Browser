package backend

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ant-chrome/backend/internal/keypouch"
)

type localGPTCredential = keypouch.LocalGPTCredential

func (a *App) readLocalGPTCredential(profileID string) (*localGPTCredential, error) {
	return keypouch.ReadLocalGPTCredential(a.appRoot, profileID)
}

func (a *App) saveLocalGPTCredential(profileID string, c localGPTCredential) error {
	return keypouch.SaveLocalGPTCredential(a.appRoot, profileID, c)
}

// BrowserProfileHasLocalGPTCredential exposes only whether the clone option applies.
func (a *App) BrowserProfileHasLocalGPTCredential(profileID string) (bool, error) {
	c, err := a.readLocalGPTCredential(profileID)
	return c != nil, err
}

// A custom browser data or core directory can encompass the OS configuration
// tree. Check traversal roots, the vault directory, and any file symlink before
// archiving them. Ordinary files in unrelated directories require no lookup.
func isGPTVaultArchiveEntry(sourceRoot, path string, entry fs.DirEntry) (bool, error) {
	if base := filepath.Base(path); base == gptSessionMarker || strings.HasPrefix(base, gptSessionMarker+"-") {
		return true, nil
	}
	if entry != nil && sourceRoot != path && entry.Type()&os.ModeSymlink == 0 &&
		(!entry.IsDir() || !strings.EqualFold(entry.Name(), keypouch.LocalGPTVaultName)) {
		return false, nil
	}
	root, err := keypouch.LocalGPTVaultDirectory()
	if err != nil {
		return false, err
	}
	root, err = filepath.EvalSymlinks(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	target, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(filepath.VolumeName(target), filepath.VolumeName(root)) {
		return false, nil
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)), nil
}
