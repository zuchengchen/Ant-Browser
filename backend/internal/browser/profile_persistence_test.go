package browser

import (
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestSingleProfileMutationsPersistOnlyTarget(t *testing.T) {
	for _, operation := range []string{"keywords", "update", "create", "copy"} {
		t.Run(operation, func(t *testing.T) {
			manager, db, dbPath := persistenceTestManager(t)
			// A DB-side change models independent group management. Saving another
			// profile must not replace that data with a stale in-memory snapshot.
			if _, err := db.GetConn().Exec(`UPDATE browser_profiles SET group_id='external-group' WHERE profile_id='untouched'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.GetConn().Exec(`CREATE TRIGGER reject_unrelated_profile_write
				BEFORE UPDATE ON browser_profiles WHEN OLD.profile_id='untouched'
				BEGIN SELECT RAISE(ABORT, 'unrelated profile must not be written'); END`); err != nil {
				t.Fatal(err)
			}
			var saved *Profile
			var err error
			switch operation {
			case "keywords":
				saved, err = manager.SetKeywords("target", []string{"new", "keywords"})
			case "update":
				saved, err = manager.Update("target", persistenceTestInput("updated"))
			case "create":
				saved, err = manager.Create(persistenceTestInput("created"))
			case "copy":
				saved, err = manager.CopyWithMode("target", "copied", "regular")
			}
			if err != nil || saved == nil {
				t.Fatalf("%s failed: profile=%v error=%v", operation, saved, err)
			}
			expected := *saved
			// Close and open the file through the production DB constructor, so
			// verification is independent of the manager and SQL connection cache.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := database.NewDB(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			dao := NewSQLiteProfileDAO(reopened.GetConn())
			actual, err := dao.GetById(expected.ProfileId)
			if err != nil || actual == nil || actual.ProfileName != expected.ProfileName || !reflect.DeepEqual(actual.Keywords, expected.Keywords) || actual.GroupId != expected.GroupId {
				t.Fatalf("reopened target=%+v error=%v expected=%+v", actual, err, expected)
			}
			untouched, err := dao.GetById("untouched")
			if err != nil || untouched.GroupId != "external-group" {
				t.Fatalf("unrelated data changed: profile=%+v error=%v", untouched, err)
			}
			reloaded := NewManager(&config.Config{}, filepath.Dir(dbPath))
			reloaded.ProfileDAO = dao
			wantCount := 2
			if operation == "create" || operation == "copy" {
				wantCount++
			}
			if got := len(reloaded.List()); got != wantCount {
				t.Fatalf("reloaded profile count=%d expected=%d", got, wantCount)
			}
		})
	}
}

func TestSingleProfileMutationsRollBackAfterWriteFailure(t *testing.T) {
	for _, operation := range []string{"keywords", "update", "create", "copy", "invalid-update"} {
		t.Run(operation, func(t *testing.T) {
			manager, db, _ := persistenceTestManager(t)
			before := manager.List()
			for _, sql := range []string{
				`CREATE TRIGGER reject_profile_insert BEFORE INSERT ON browser_profiles BEGIN SELECT RAISE(ABORT, 'test persistence failure'); END`,
				`CREATE TRIGGER reject_profile_update BEFORE UPDATE ON browser_profiles BEGIN SELECT RAISE(ABORT, 'test persistence failure'); END`,
			} {
				if _, err := db.GetConn().Exec(sql); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch operation {
			case "keywords":
				_, err = manager.SetKeywords("target", []string{"must-not-remain"})
			case "update":
				_, err = manager.Update("target", persistenceTestInput("must-not-remain"))
			case "create":
				_, err = manager.Create(persistenceTestInput("must-not-remain"))
			case "copy":
				_, err = manager.CopyWithMode("target", "must-not-remain", "regular")
			case "invalid-update":
				_, err = manager.Update("target", ProfileInput{ProfileName: "invalid-no-proxy"})
			}
			if err == nil {
				t.Fatal("mutation unexpectedly succeeded")
			}
			if after := manager.List(); !reflect.DeepEqual(before, after) {
				t.Fatalf("failed operation left in-memory changes: before=%+v after=%+v", before, after)
			}
			persisted, err := manager.ProfileDAO.List()
			if err != nil || len(persisted) != 2 {
				t.Fatalf("database changed after failure: count=%d error=%v", len(persisted), err)
			}
			for _, profile := range persisted {
				if profile.ProfileName != profile.ProfileId || !reflect.DeepEqual(profile.Keywords, []string{"original"}) {
					t.Fatalf("failed operation persisted data: %+v", profile)
				}
			}
		})
	}
}

func TestSingleProfileFilePersistenceRetainsAllProfilesAndRollsBackFailure(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(&config.Config{}, root)
	for _, id := range []string{"target", "untouched"} {
		manager.Profiles[id] = persistenceTestProfile(id)
	}
	if _, err := manager.SetKeywords("target", []string{"file-saved"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(filepath.Join(root, "config.yaml"))
	if err != nil || len(loaded.Browser.Profiles) != 2 {
		t.Fatalf("file snapshot incomplete: config=%v error=%v", loaded, err)
	}
	before := manager.List()
	previousConfigProfiles := append([]config.BrowserProfileConfig(nil), manager.Config.Browser.Profiles...)
	if err := os.Rename(filepath.Join(root, "config.yaml"), filepath.Join(root, "saved.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "config.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update("target", persistenceTestInput("must-not-remain")); err == nil {
		t.Fatal("expected filesystem write failure")
	}
	if !reflect.DeepEqual(manager.List(), before) || !reflect.DeepEqual(manager.Config.Browser.Profiles, previousConfigProfiles) {
		t.Fatal("failed file save changed the manager or config snapshot")
	}
}

func TestSingleProfileKeywordWritesAndListsRemainConsistent(t *testing.T) {
	manager, _, _ := persistenceTestManager(t)
	var workers sync.WaitGroup
	for index := 0; index < 4; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			for iteration := 0; iteration < 10; iteration++ {
				if _, err := manager.SetKeywords("target", []string{fmt.Sprintf("worker-%d-%d", index, iteration)}); err != nil {
					t.Errorf("keyword update: %v", err)
					return
				}
				if profiles := manager.List(); len(profiles) != 2 {
					t.Errorf("unexpected list count: %d", len(profiles))
				}
			}
		}(index)
	}
	workers.Wait()
	stored, err := manager.ProfileDAO.GetById("target")
	if err != nil || !reflect.DeepEqual(stored.Keywords, manager.Profiles["target"].Keywords) {
		t.Fatalf("last completed write is inconsistent: profile=%v error=%v", stored, err)
	}
}

func persistenceTestManager(t *testing.T) (*Manager, *database.DB, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "profiles.db")
	db, err := database.NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(&config.Config{}, root)
	manager.ProfileDAO = NewSQLiteProfileDAO(db.GetConn())
	for _, id := range []string{"target", "untouched"} {
		if err := manager.ProfileDAO.Upsert(persistenceTestProfile(id)); err != nil {
			t.Fatal(err)
		}
	}
	manager.InitData()
	return manager, db, path
}

func persistenceTestProfile(id string) *Profile {
	return &Profile{
		ProfileId: id, ProfileName: id, UserDataDir: id, CoreId: "perf-core",
		ProxyMode: ProxyModeLocal, ProxyConfig: "http://127.0.0.1:18080",
		FingerprintArgs: []string{"--fingerprint=123456"}, LaunchArgs: []string{},
		Tags: []string{"original"}, Keywords: []string{"original"}, GroupId: "original-group",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
}

func persistenceTestInput(name string) ProfileInput {
	return ProfileInput{
		ProfileName: name, UserDataDir: "edited-dir", CoreId: "edited-core",
		ProxyMode: ProxyModeLocal, ProxyConfig: "http://127.0.0.1:18081",
		FingerprintArgs: []string{"--fingerprint=654321"}, LaunchArgs: []string{"--lang=en-US"},
		Tags: []string{"edited"}, Keywords: []string{"edited"}, GroupId: "edited-group", MemoryLimitMB: 768,
	}
}
