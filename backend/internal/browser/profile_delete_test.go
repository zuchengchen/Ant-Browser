package browser

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
)

func TestDeleteRejectsRunningProfile(t *testing.T) {
	manager := NewManager(config.DefaultConfig(), t.TempDir())
	profile := &Profile{ProfileId: "profile-running", Running: true}
	manager.Profiles[profile.ProfileId] = profile

	err := manager.Delete(profile.ProfileId)
	if !errors.Is(err, ErrProfileRunning) {
		t.Fatalf("Delete() error = %v, want ErrProfileRunning", err)
	}
	if _, exists := manager.Profiles[profile.ProfileId]; !exists {
		t.Fatal("Delete() removed a running profile")
	}
	if profile.DeletedAt != "" {
		t.Fatalf("Delete() set DeletedAt = %q for a running profile", profile.DeletedAt)
	}
}

func TestDeleteRejectsProfileWithActiveProcess(t *testing.T) {
	manager := NewManager(config.DefaultConfig(), t.TempDir())
	profile := &Profile{ProfileId: "profile-starting"}
	manager.Profiles[profile.ProfileId] = profile
	manager.BrowserProcesses[profile.ProfileId] = &exec.Cmd{}

	err := manager.Delete(profile.ProfileId)
	if !errors.Is(err, ErrProfileRunning) {
		t.Fatalf("Delete() error = %v, want ErrProfileRunning", err)
	}
	if _, exists := manager.Profiles[profile.ProfileId]; !exists {
		t.Fatal("Delete() removed a profile with an active process")
	}
}

func TestDeleteRejectsDetectedRuntime(t *testing.T) {
	manager := NewManager(config.DefaultConfig(), t.TempDir())
	profile := &Profile{ProfileId: "profile-detected-runtime"}
	manager.Profiles[profile.ProfileId] = profile
	manager.ProfileRuntimeDetector = func(candidate *Profile) bool {
		return candidate != nil && candidate.ProfileId == profile.ProfileId
	}

	err := manager.Delete(profile.ProfileId)
	if !errors.Is(err, ErrProfileRunning) {
		t.Fatalf("Delete() error = %v, want ErrProfileRunning", err)
	}
	if _, exists := manager.Profiles[profile.ProfileId]; !exists {
		t.Fatal("Delete() removed a profile with a detected runtime")
	}
}

func TestRecycleBinRestoresLegacyProfileAfterPreviousRetentionPeriod(t *testing.T) {
	appRoot := t.TempDir()
	db, err := database.NewDB(filepath.Join(appRoot, "profiles.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	profiles := NewSQLiteProfileDAO(db.GetConn())
	profile := &Profile{ProfileId: "legacy-trash", ProfileName: "Legacy profile", UserDataDir: "legacy-trash"}
	if err := profiles.Upsert(profile); err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Now().AddDate(0, -1, 0).Format(time.RFC3339)
	if err := profiles.SoftDelete(profile.ProfileId, deletedAt); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = "data"
	manager := NewManager(cfg, appRoot)
	manager.ProfileDAO = profiles
	profileData := filepath.Join(manager.ResolveUserDataDir(profile), "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(profileData), 0755); err != nil {
		t.Fatal(err)
	}
	const originalPreferences = `{"profile":{"name":"Legacy profile"}}`
	if err := os.WriteFile(profileData, []byte(originalPreferences), 0600); err != nil {
		t.Fatal(err)
	}

	trash := manager.ListDeleted()
	if len(trash) != 1 || trash[0].ProfileId != profile.ProfileId || trash[0].DeletedAt != deletedAt {
		t.Fatalf("ListDeleted() = %#v, want the original trash entry", trash)
	}
	if _, err := manager.Restore(profile.ProfileId); err != nil {
		t.Fatal(err)
	}
	active, err := profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ProfileId != profile.ProfileId || active[0].DeletedAt != "" {
		t.Fatalf("List() = %#v, want the restored profile", active)
	}
	preferences, err := os.ReadFile(profileData)
	if err != nil {
		t.Fatal(err)
	}
	if string(preferences) != originalPreferences {
		t.Fatalf("profile data = %q, want %q", preferences, originalPreferences)
	}
}

func TestPermanentDeletePreservesDirectoriesReferencedByOtherProfiles(t *testing.T) {
	cases := []struct {
		name      string
		removed   string
		protected string
		inTrash   bool
		symlink   bool
	}{
		{name: "same directory", removed: "shared", protected: "shared"},
		{name: "nested directory", removed: "shared", protected: "shared/child"},
		{name: "parent directory", removed: "shared/child", protected: "shared"},
		{name: "trash entry can still be restored", removed: "shared", protected: "shared", inTrash: true},
		{name: "symbolic alias", removed: "shared", protected: "alias", symlink: true},
		{name: "custom directory in snapshot tree", removed: "standalone", protected: "snapshots/selected"},
		{name: "custom directory in fingerprint cache tree", removed: "standalone", protected: "fingerprint-check/selected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			db, err := database.NewDB(filepath.Join(root, "profiles.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := db.Migrate(); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(config.DefaultConfig(), root)
			manager.ProfileDAO = NewSQLiteProfileDAO(db.GetConn())
			selected := &Profile{ProfileId: "selected", ProfileName: "Selected", UserDataDir: tc.removed}
			other := &Profile{ProfileId: "other", ProfileName: "Other", UserDataDir: tc.protected}
			for _, profile := range []*Profile{selected, other} {
				if err := manager.ProfileDAO.Upsert(profile); err != nil {
					t.Fatal(err)
				}
			}
			if tc.inTrash {
				if err := manager.Delete(other.ProfileId); err != nil {
					t.Fatal(err)
				}
			}
			dataDir := manager.ResolveUserDataDir(other)
			if tc.symlink {
				sharedDir := manager.ResolveUserDataDir(selected)
				if err := os.MkdirAll(sharedDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sharedDir, dataDir); err != nil {
					t.Skipf("symbolic links unavailable: %v", err)
				}
			}
			preferences := filepath.Join(dataDir, "Default", "Preferences")
			if err := os.MkdirAll(filepath.Dir(preferences), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(preferences, []byte("other profile"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := manager.Delete(selected.ProfileId); err != nil {
				t.Fatal(err)
			}
			if err := manager.PermanentlyDelete(selected.ProfileId); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ProfileDAO.GetById(selected.ProfileId); err == nil {
				t.Fatal("deleted profile still exists")
			}
			if tc.inTrash {
				if _, err := manager.Restore(other.ProfileId); err != nil {
					t.Fatal(err)
				}
			}
			if data, err := os.ReadFile(preferences); err != nil || string(data) != "other profile" {
				t.Fatalf("other profile lost its data: %q, %v", data, err)
			}
		})
	}
}
