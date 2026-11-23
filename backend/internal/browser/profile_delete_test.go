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
