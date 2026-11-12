//go:build linux

package browser_test

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"ant-chrome/backend/internal/launchcode"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// BenchmarkLinuxPerfProfiles measures the same Manager operations used by the
// Wails profile list, keyword editor and profile editor against on-disk SQLite.
// Fixture insertion is excluded from timing; measured saves retain the
// application's WAL and synchronous defaults and are read back after timing.
func BenchmarkLinuxPerfProfiles(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", count), func(b *testing.B) {
			b.Run("SetKeywords", func(b *testing.B) {
				manager, dao := linuxPerfProfileFixture(b, count)
				id := linuxPerfProfileID(count / 2)
				var expected []string
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					expected = []string{"linux-perf", fmt.Sprintf("saved-%d", i)}
					profile, err := manager.SetKeywords(id, expected)
					if err != nil || profile == nil || profile.ProfileId != id {
						b.Fatalf("SetKeywords did not return target: profile=%v error=%v", profile, err)
					}
				}
				b.StopTimer()
				stored, err := dao.GetById(id)
				if err != nil || stored == nil || !linuxPerfEqualStrings(stored.Keywords, expected) {
					b.Fatalf("saved keywords mismatch: profile=%v error=%v expected=%v", stored, err, expected)
				}
				linuxPerfVerifyProfileReload(b, manager, count, id, expected)
			})
			b.Run("Update", func(b *testing.B) {
				manager, dao := linuxPerfProfileFixture(b, count)
				id := linuxPerfProfileID(count / 2)
				input := browser.ProfileInput{
					UserDataDir: id, CoreId: "perf-core", ProxyMode: browser.ProxyModeLocal,
					ProxyConfig: "http://127.0.0.1:18080", FingerprintArgs: []string{"--fingerprint=123456"},
					LaunchArgs: []string{"--lang=en-US"}, Tags: []string{"perf", "edited"},
					Keywords: []string{"linux-perf", "edited"}, GroupId: "group-3", MemoryLimitMB: 768,
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					input.ProfileName = fmt.Sprintf("Edited profile %d", i)
					profile, err := manager.Update(id, input)
					if err != nil || profile == nil || profile.ProfileName != input.ProfileName {
						b.Fatalf("Update did not return target: profile=%v error=%v", profile, err)
					}
				}
				b.StopTimer()
				stored, err := dao.GetById(id)
				if err != nil || stored == nil || stored.ProfileName != input.ProfileName || stored.GroupId != input.GroupId || stored.MemoryLimitMB != input.MemoryLimitMB {
					b.Fatalf("saved profile mismatch: profile=%v error=%v input=%v", stored, err, input)
				}
				linuxPerfVerifyProfileReload(b, manager, count, id, input.Keywords)
			})
			b.Run("List", func(b *testing.B) {
				manager, _ := linuxPerfProfileFixture(b, count)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					profiles := manager.List()
					if len(profiles) != count || profiles[0].ProfileId != linuxPerfProfileID(0) || profiles[count-1].ProfileId != linuxPerfProfileID(count-1) || profiles[count-1].LaunchCode == "" {
						b.Fatalf("List returned incomplete or unordered profiles: count=%d", len(profiles))
					}
				}
			})
			b.Run("ListByTag", func(b *testing.B) {
				manager, _ := linuxPerfProfileFixture(b, count)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					profiles := manager.ListByTag("selected")
					if len(profiles) != count/10 || profiles[0].ProfileId != linuxPerfProfileID(0) || profiles[len(profiles)-1].ProfileId != linuxPerfProfileID(count-10) {
						b.Fatalf("ListByTag returned unexpected selection: count=%d", len(profiles))
					}
				}
			})
			b.Run("DAOList", func(b *testing.B) {
				_, dao := linuxPerfProfileFixture(b, count)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					profiles, err := dao.List()
					if err != nil || len(profiles) != count {
						b.Fatalf("DAO List failed: count=%d error=%v", len(profiles), err)
					}
				}
			})
		})
	}
}

func linuxPerfProfileFixture(tb testing.TB, count int) (*browser.Manager, *browser.SQLiteProfileDAO) {
	tb.Helper()
	root := tb.TempDir()
	db, err := database.NewDB(filepath.Join(root, "profiles.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := db.Close(); err != nil {
			tb.Error(err)
		}
	})
	if err := db.Migrate(); err != nil {
		tb.Fatal(err)
	}
	// Bulk fixture creation does not measure or replace any product operation.
	// Both revisions use the same deterministic rows and production schema.
	tx, err := db.GetConn().Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	profileStmt, err := tx.Prepare(`INSERT INTO browser_profiles
		(profile_id, profile_name, user_data_dir, core_id, fingerprint_args,
		 proxy_id, proxy_config, proxy_mode, launch_args, tags, keywords, group_id,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer profileStmt.Close()
	codeStmt, err := tx.Prepare(`INSERT INTO launch_codes (profile_id, code) VALUES (?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer codeStmt.Close()
	for i := 0; i < count; i++ {
		id := linuxPerfProfileID(i)
		tags := []string{"perf", fmt.Sprintf("bucket-%d", i%10)}
		if i%10 == 0 {
			tags = append(tags, "selected")
		}
		tagsJSON, err := json.Marshal(tags)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := profileStmt.Exec(id, fmt.Sprintf("Linux performance profile %05d", i), id,
			"perf-core", `["--fingerprint=123456"]`, "", "http://127.0.0.1:18080",
			browser.ProxyModeLocal, `["--lang=en-US"]`, string(tagsJSON), `["synthetic","baseline"]`,
			fmt.Sprintf("group-%d", i%20), "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			tb.Fatal(err)
		}
		if _, err := codeStmt.Exec(id, fmt.Sprintf("P%05d", i)); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	dao := browser.NewSQLiteProfileDAO(db.GetConn())
	manager := browser.NewManager(&config.Config{}, root)
	manager.ProfileDAO = dao
	codeService := launchcode.NewLaunchCodeService(launchcode.NewSQLiteLaunchCodeDAO(db.GetConn()))
	if err := codeService.LoadAll(); err != nil {
		tb.Fatal(err)
	}
	manager.CodeProvider = codeService
	manager.InitData()
	if got := len(manager.List()); got != count {
		tb.Fatalf("fixture profile count=%d expected=%d", got, count)
	}
	return manager, dao
}

func linuxPerfVerifyProfileReload(tb testing.TB, manager *browser.Manager, count int, id string, keywords []string) {
	tb.Helper()
	reloaded := browser.NewManager(&config.Config{}, manager.AppRoot)
	reloaded.ProfileDAO = manager.ProfileDAO
	profiles := reloaded.List()
	if len(profiles) != count {
		tb.Fatalf("reload profile count=%d expected=%d", len(profiles), count)
	}
	for _, profile := range profiles {
		if profile.ProfileId == id {
			if !linuxPerfEqualStrings(profile.Keywords, keywords) {
				tb.Fatalf("reload keywords=%v expected=%v", profile.Keywords, keywords)
			}
			return
		}
	}
	tb.Fatalf("reload did not find target %q", id)
}

func linuxPerfProfileID(index int) string {
	return fmt.Sprintf("perf-profile-%05d", index)
}

func linuxPerfEqualStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
