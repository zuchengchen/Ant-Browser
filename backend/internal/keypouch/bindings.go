package keypouch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Profiles created by this version already have a stable ID. Keep only the
// exceptions: existing local profiles the user explicitly associated after an
// upgrade. Desktop link operations are serialized by the Ant primary process.
type ProfileLinks struct {
	path    string
	records map[int64]string
}

func (d *Device) LoadProfileLinks() (*ProfileLinks, error) {
	if d.stateDir == "" {
		return nil, errors.New("缺少本机配对目录")
	}
	links := &ProfileLinks{path: filepath.Join(d.stateDir, "profile-links.json"), records: make(map[int64]string)}
	b, err := os.ReadFile(links.path)
	if errors.Is(err, os.ErrNotExist) {
		return links, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &links.records); err != nil || links.records == nil {
		return nil, errors.New("本机实例关联文件无效")
	}
	return links, nil
}

func (l *ProfileLinks) ProfileID(record int64) string { return l.records[record] }

func (l *ProfileLinks) Available(record int64, profile string) bool {
	for r, p := range l.records {
		if r != record && p == profile {
			return false
		}
	}
	return l.records[record] == "" || l.records[record] == profile
}

func (l *ProfileLinks) Bind(record int64, profile string) error {
	if !l.Available(record, profile) {
		return errors.New("该实例已关联其他记录")
	}
	if l.records[record] == profile {
		return nil
	}
	next := make(map[int64]string, len(l.records)+1)
	for r, p := range l.records {
		next[r] = p
	}
	next[record] = profile
	f, err := os.CreateTemp(filepath.Dir(l.path), ".profile-links-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(next)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), l.path); err != nil {
		return err
	}
	l.records = next
	return nil
}
