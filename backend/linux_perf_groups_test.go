//go:build linux

package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"fmt"
	"path/filepath"
	"testing"
)

// BenchmarkLinuxPerfGroupList measures the actual Wails group-list entry point.
// The synthetic directory includes active, deleted and ungrouped profiles, so a
// faster aggregate cannot pass by silently counting all database rows.
func BenchmarkLinuxPerfGroupList(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("n=%d", count), func(b *testing.B) {
			app, _ := linuxPerfGroupFixture(b, count)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				groups := app.ListGroups()
				if len(groups) != 21 {
					b.Fatalf("group count=%d expected=21", len(groups))
				}
				for index, group := range groups {
					expected := count / 20
					if index == 20 {
						expected = 0
					}
					if group.GroupId != fmt.Sprintf("perf-group-%02d", index) || group.InstanceCount != expected {
						b.Fatalf("group=%+v expected index=%d count=%d", group, index, expected)
					}
				}
			}
		})
	}
}

func linuxPerfGroupFixture(tb testing.TB, count int) (*App, *database.DB) {
	tb.Helper()
	root := tb.TempDir()
	db, err := database.NewDB(filepath.Join(root, "groups.db"))
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
	tx, err := db.GetConn().Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 21; i++ {
		if _, err := tx.Exec(`INSERT INTO browser_groups (group_id, group_name, sort_order) VALUES (?, ?, ?)`,
			fmt.Sprintf("perf-group-%02d", i), fmt.Sprintf("Performance group %d", i), i); err != nil {
			tb.Fatal(err)
		}
	}
	stmt, err := tx.Prepare(`INSERT INTO browser_profiles
		(profile_id, profile_name, fingerprint_args, launch_args, tags, keywords,
		 group_id, deleted_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < count+20; i++ {
		groupID := fmt.Sprintf("perf-group-%02d", i%20)
		deletedAt := ""
		if i >= count && i < count+10 {
			deletedAt = "2026-01-02T00:00:00Z"
		} else if i >= count+10 {
			groupID = ""
		}
		if _, err := stmt.Exec(fmt.Sprintf("perf-profile-%05d", i), fmt.Sprintf("Linux group fixture %d", i),
			`["--fingerprint=123456"]`, `["--lang=en-US"]`, `["perf","group-fixture"]`,
			`["synthetic","baseline"]`, groupID, deletedAt, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	manager := browser.NewManager(&config.Config{}, root)
	manager.ProfileDAO = browser.NewSQLiteProfileDAO(db.GetConn())
	manager.GroupDAO = browser.NewSQLiteGroupDAO(db.GetConn())
	return &App{browserMgr: manager}, db
}
