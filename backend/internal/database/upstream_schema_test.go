package database

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These short fixtures contain only synthetic values. Versions 36–40 create
// opaque historical tables; the production migration must never replay them.
var localSchemaFixtures = []struct {
	desc, sql string
}{
	{"保存实例加密的 GPT 自动登录凭据", `
		CREATE TABLE browser_chatgpt_credentials (profile_id TEXT PRIMARY KEY REFERENCES browser_profiles(profile_id) ON DELETE CASCADE, ciphertext BLOB NOT NULL, updated_at TEXT NOT NULL);
		INSERT INTO browser_chatgpt_credentials VALUES ('active', x'0001FF', 'synthetic');`},
	{"保存 GPT 自动续费检查结果与跨实例操作记录", `
		CREATE TABLE browser_chatgpt_renewal (profile_id TEXT PRIMARY KEY REFERENCES browser_profiles(profile_id) ON DELETE CASCADE, snapshot_json TEXT NOT NULL);
		CREATE TABLE browser_chatgpt_renewal_accounts (email TEXT PRIMARY KEY COLLATE NOCASE, state TEXT NOT NULL, snapshot_json TEXT NOT NULL);
		INSERT INTO browser_chatgpt_renewal VALUES ('trash','{"fixture":37}');
		INSERT INTO browser_chatgpt_renewal_accounts VALUES ('fixture@example.invalid','synthetic','{}');`},
	{"记录 KeyPouch 本机实例关联和只读浏览器会话", `
		CREATE TABLE browser_keypouch_profiles (device_id TEXT NOT NULL, record_id INTEGER NOT NULL, email TEXT NOT NULL, profile_id TEXT NOT NULL REFERENCES browser_profiles(profile_id) ON DELETE CASCADE, rejected INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(device_id,record_id,profile_id));
		CREATE UNIQUE INDEX browser_keypouch_record ON browser_keypouch_profiles(device_id,record_id) WHERE rejected=0;
		CREATE UNIQUE INDEX browser_keypouch_profile ON browser_keypouch_profiles(device_id,profile_id) WHERE rejected=0;
		CREATE TABLE browser_keypouch_readonly (profile_id TEXT PRIMARY KEY REFERENCES browser_profiles(profile_id) ON DELETE CASCADE);
		INSERT INTO browser_keypouch_profiles VALUES ('fixture',1,'fixture@example.invalid','active',0);
		INSERT INTO browser_keypouch_readonly VALUES ('active');`},
	{"复用当前实例的 KeyPouch 登录和聊天标签页", `
		ALTER TABLE browser_keypouch_readonly ADD COLUMN pending_tab TEXT NOT NULL DEFAULT '';
		ALTER TABLE browser_keypouch_readonly ADD COLUMN pending_email TEXT NOT NULL DEFAULT '';
		ALTER TABLE browser_keypouch_readonly ADD COLUMN chat_tab TEXT NOT NULL DEFAULT '';
		ALTER TABLE browser_keypouch_readonly ADD COLUMN chat_email TEXT NOT NULL DEFAULT '';
		UPDATE browser_keypouch_readonly SET chat_tab='fixture-tab';`},
	{"在旧版配置导入后一次性初始化 KeyPouch 实例模板", `
		CREATE TABLE browser_keypouch_template_init (singleton INTEGER PRIMARY KEY CHECK(singleton=1));
		INSERT INTO browser_keypouch_template_init VALUES (1);`},
}

func makeBranchFixture(t *testing.T, version int, upstream, existingProxyError bool) (*DB, string) {
	t.Helper()
	db, path := instanceGroupsLegacyDB(t)
	instanceGroupsLegacyGroup(t, db, "root", "Work", "")
	instanceGroupsLegacyGroup(t, db, "child", "Child", "root")
	instanceGroupsLegacyProfile(t, db, "active", "child", []string{"Work", "Legacy"}, false)
	instanceGroupsLegacyProfile(t, db, "trash", "child", []string{"Archived"}, true)
	instanceGroupsLegacyExec(t, db, `UPDATE browser_profiles SET fingerprint_args='["fixture"]', keywords='["keep"]', proxy_config='direct://'`)
	if upstream || existingProxyError {
		instanceGroupsLegacyExec(t, db, `ALTER TABLE browser_profiles ADD COLUMN proxy_bind_error TEXT NOT NULL DEFAULT ''`)
		instanceGroupsLegacyExec(t, db, `UPDATE browser_profiles SET proxy_bind_error='fixture proxy unavailable' WHERE profile_id='active'`)
	}
	if upstream {
		instanceGroupsLegacyExec(t, db, `INSERT INTO schema_migrations(version,desc,applied_at) VALUES(34,?,'2026-01-01')`, upstreamProxy34Description)
		return db, path
	}
	if version >= 34 {
		if err := db.applyMigration(forkGroups34); err != nil {
			t.Fatal(err)
		}
		// Canonical assignments changed after upgrading from v33. Replaying
		// the legacy columns would resurrect a removed group and a stale tag.
		instanceGroupsLegacyExec(t, db, `DELETE FROM browser_profile_groups WHERE profile_id='active' AND group_id='root'`)
		instanceGroupsLegacyGroup(t, db, "manual", "Manual", "")
		instanceGroupsLegacyExec(t, db, `INSERT INTO browser_profile_groups VALUES ('active','manual')`)
		instanceGroupsLegacyExec(t, db, `UPDATE browser_profiles SET tags='["obsolete tag"]' WHERE profile_id='active'`)
	}
	if version >= 35 {
		if err := db.applyMigration(forkSnapshots35); err != nil {
			t.Fatal(err)
		}
		instanceGroupsLegacyExec(t, db, `INSERT INTO browser_chatgpt_accounts VALUES ('active','{ "fixture" : "keep" }'), ('trash','{"archived":true}')`)
	}
	for i, fixture := range localSchemaFixtures {
		if n := i + 36; n <= version {
			instanceGroupsLegacyExec(t, db, fixture.sql)
			instanceGroupsLegacyExec(t, db, `INSERT INTO schema_migrations(version,desc) VALUES(?,?)`, n, fixture.desc)
		}
	}
	return db, path
}

func fixtureRows(t *testing.T, conn *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := conn.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func localFixtureTables(t *testing.T, db *DB) string {
	t.Helper()
	rows, err := db.conn.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND
		(name LIKE 'browser_keypouch_%' OR (name LIKE 'browser_chatgpt_%' AND name != 'browser_chatgpt_accounts'))
		ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	var state strings.Builder
	for _, name := range names {
		state.WriteString(fixtureRows(t, db.conn, `SELECT type, name, sql FROM sqlite_schema WHERE tbl_name=? ORDER BY name`, name))
		state.WriteString(fixtureRows(t, db.conn, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`" ORDER BY rowid`))
	}
	return state.String()
}

const fixtureHistory = `SELECT version,desc,CAST(applied_at AS TEXT) FROM schema_migrations WHERE version<=? ORDER BY version`
const fixtureProfiles = `SELECT profile_id,profile_name,group_id,tags,deleted_at,proxy_config,keywords,fingerprint_args FROM browser_profiles ORDER BY profile_id`
const fixtureMemberships = `SELECT p.profile_id,g.group_name FROM browser_profile_groups p JOIN browser_groups g USING(group_id) ORDER BY p.profile_id,g.group_name`
const fixtureSnapshots = `SELECT profile_id,snapshot_json FROM browser_chatgpt_accounts ORDER BY profile_id`

func TestUpstreamSchema41UpgradeMatrix(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		version              int
		upstream, savedProxy bool
	}{
		{"fresh", 0, false, false}, {"common-33", 33, false, false}, {"upstream-34", 34, true, true},
		{"fork-34", 34, false, false}, {"fork-35", 35, false, false}, {"fork-35-existing-proxy-error", 35, false, true},
		{"development-36", 36, false, false}, {"development-37", 37, false, false}, {"development-38", 38, false, false},
		{"development-39", 39, false, false}, {"development-40", 40, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fresh.db")
			var originalHistory, profiles, extras string
			accounts := "null"
			if tc.version != 0 {
				seed, fixturePath := makeBranchFixture(t, tc.version, tc.upstream, tc.savedProxy)
				path = fixturePath
				originalHistory = fixtureRows(t, seed.conn, fixtureHistory, tc.version)
				profiles = fixtureRows(t, seed.conn, fixtureProfiles)
				extras = localFixtureTables(t, seed)
				if tc.version >= 35 {
					accounts = fixtureRows(t, seed.conn, fixtureSnapshots)
				}
				if err := seed.Close(); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(filepath.Dir(path), "backups")
			var firstHistory, firstGroups string
			for attempt := 1; attempt <= 2; attempt++ {
				db, info, err := OpenWithRecovery(path, dir)
				if err != nil || info.Recovered {
					t.Fatalf("open %d: info=%+v err=%v", attempt, info, err)
				}
				defer db.Close()
				if wantBackup := tc.version > 0 && attempt == 1; wantBackup != (info.MigrationBackup != "") {
					t.Fatalf("open %d: backup=%q", attempt, info.MigrationBackup)
				}
				if got := fixtureRows(t, db.conn, `SELECT version FROM schema_migrations WHERE version>=41`); got != "[[41]]" {
					t.Fatalf("new migration=%s", got)
				}
				if got := fixtureRows(t, db.conn, `SELECT COUNT(*) FROM schema_migrations WHERE version BETWEEN 36 AND 40`); got != fmt.Sprintf("[[%d]]", max(0, tc.version-35)) {
					t.Fatalf("reserved migration history=%s", got)
				}
				if got := localFixtureTables(t, db); got != extras {
					t.Fatalf("development schema or contents changed: %s -> %s", extras, got)
				}
				if got := fixtureRows(t, db.conn, fixtureSnapshots); got != accounts {
					t.Fatalf("snapshots=%s want %s", got, accounts)
				}
				history := fixtureRows(t, db.conn, fixtureHistory, 41)
				groups := fixtureRows(t, db.conn, `SELECT group_id,group_name,parent_id FROM browser_groups ORDER BY group_id`)
				if tc.version != 0 {
					if got := fixtureRows(t, db.conn, fixtureHistory, tc.version); got != originalHistory {
						t.Fatalf("preexisting migration records changed: %s -> %s", originalHistory, got)
					}
					if got := fixtureRows(t, db.conn, fixtureProfiles); got != profiles {
						t.Fatalf("profiles or recycle bin changed: %s -> %s", profiles, got)
					}
					want := `[["active","Child"],["active","Legacy"],["active","Work"],["trash","Archived"],["trash","Child"]]`
					if !tc.upstream && tc.version >= 34 {
						want = `[["active","Child"],["active","Legacy"],["active","Manual"],["trash","Archived"],["trash","Child"]]`
					}
					if got := fixtureRows(t, db.conn, fixtureMemberships); got != want {
						t.Fatalf("relationships=%s want %s", got, want)
					}
					wantError := `[[""],[""]]`
					if tc.savedProxy {
						wantError = `[["fixture proxy unavailable"],[""]]`
					}
					if got := fixtureRows(t, db.conn, `SELECT proxy_bind_error FROM browser_profiles ORDER BY profile_id`); got != wantError {
						t.Fatalf("proxy errors=%s want %s", got, wantError)
					}
				} else if !sqliteColumnExists(t, db.conn, "browser_profiles", "proxy_bind_error") {
					t.Fatal("missing proxy_bind_error on fresh database")
				}
				if attempt == 1 {
					firstHistory, firstGroups = history, groups
				} else if history != firstHistory || groups != firstGroups {
					t.Fatal("second startup rewrote history or groups")
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestUpstreamSchema41WALBackupAndRestore(t *testing.T) {
	seed, path := makeBranchFixture(t, 34, true, true)
	instanceGroupsLegacyExec(t, seed, `PRAGMA wal_autocheckpoint=0`)
	var busy, pages, checkpointed int
	if err := seed.conn.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &pages, &checkpointed); err != nil || busy != 0 {
		t.Fatalf("checkpoint: busy=%d, err=%v", busy, err)
	}
	instanceGroupsLegacyProfile(t, seed, "wal-only", "child", []string{"WAL"}, true)
	instanceGroupsLegacyExec(t, seed, `UPDATE browser_profiles SET proxy_bind_error='wal-only-error' WHERE profile_id='wal-only'`)
	if stat, err := os.Stat(path + "-wal"); err != nil || stat.Size() <= 32 {
		t.Fatalf("WAL fixture missing: %v %v", stat, err)
	}
	// Opening a physical copy of only the main file proves the fixture rows
	// cannot be recovered by a backup implementation that ignores the WAL.
	mainOnly := filepath.Join(t.TempDir(), "main-only.db")
	if err := copyDatabaseFile(path, mainOnly); err != nil {
		t.Fatal(err)
	}
	withoutWAL, err := sql.Open("sqlite", mainOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer withoutWAL.Close()
	if got := fixtureRows(t, withoutWAL, `SELECT COUNT(*) FROM browser_profiles WHERE profile_id='wal-only'`); got != "[[0]]" {
		t.Fatalf("fixture already checkpointed: %s", got)
	}
	dir := filepath.Join(filepath.Dir(path), "backups")
	db, info, err := OpenWithRecovery(path, dir)
	if err != nil || info.Recovered {
		t.Fatalf("open: %+v %v", info, err)
	}
	defer db.Close()
	if got := fixtureRows(t, db.conn, `SELECT g.group_name FROM browser_profile_groups p JOIN browser_groups g USING(group_id) WHERE p.profile_id='wal-only' ORDER BY 1`); got != `[["Child"],["WAL"]]` {
		t.Fatalf("WAL relationships=%s", got)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	if err := copyDatabaseFile(info.MigrationBackup, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := fixtureRows(t, restored, `PRAGMA integrity_check`); got != `[["ok"]]` {
		t.Fatalf("restored integrity=%s", got)
	}
	if got := fixtureRows(t, restored, `SELECT version,desc FROM schema_migrations WHERE version>=34 ORDER BY version`); got != fmt.Sprintf(`[[34,"%s"]]`, upstreamProxy34Description) {
		t.Fatalf("restored schema/history is not upstream v34: %s", got)
	}
	if got := fixtureRows(t, restored, `SELECT name FROM sqlite_schema WHERE name IN ('browser_profile_groups','browser_chatgpt_accounts')`); got != "null" {
		t.Fatalf("restored tables differ from upstream v34: %s", got)
	}
	if got := fixtureRows(t, restored, `SELECT group_id,tags,proxy_bind_error,deleted_at FROM browser_profiles WHERE profile_id='wal-only'`); got != `[["child","[\"WAL\"]","wal-only-error","2026-11-12T00:00:00Z"]]` {
		t.Fatalf("restored WAL data=%s", got)
	}
}

func TestUpstreamSchema41MigrationRollbackAndRetry(t *testing.T) {
	seed, path := makeBranchFixture(t, 34, true, true)
	dir := filepath.Join(filepath.Dir(path), "backups")
	instanceGroupsLegacyExec(t, seed, `CREATE TRIGGER stop_bridge BEFORE INSERT ON schema_migrations WHEN NEW.version=41 BEGIN SELECT RAISE(ABORT,'synthetic bridge failure'); END`)
	history := fixtureRows(t, seed.conn, fixtureHistory, 41)
	profiles := fixtureRows(t, seed.conn, fixtureProfiles)
	groups := fixtureRows(t, seed.conn, `SELECT * FROM browser_groups ORDER BY group_id`)
	db, info, err := OpenWithRecovery(path, dir)
	if err == nil || db != nil || info.Recovered || info.MigrationBackup == "" || !strings.Contains(err.Error(), "synthetic bridge failure") {
		t.Fatalf("expected transactional error with backup: %+v %v", info, err)
	}
	if got := fixtureRows(t, seed.conn, `SELECT name FROM sqlite_schema WHERE name IN ('browser_profile_groups','idx_browser_groups_sibling_name','browser_chatgpt_accounts')`); got != "null" {
		t.Fatalf("partially committed tables/indexes: %s", got)
	}
	if fixtureRows(t, seed.conn, fixtureHistory, 41) != history || fixtureRows(t, seed.conn, fixtureProfiles) != profiles || fixtureRows(t, seed.conn, `SELECT * FROM browser_groups ORDER BY group_id`) != groups {
		t.Fatal("failed migration changed source data or migration history")
	}
	checkNoParkedDatabase(t, path)
	instanceGroupsLegacyExec(t, seed, `DROP TRIGGER stop_bridge`)
	db, retry, err := OpenWithRecovery(path, dir)
	if err != nil || retry.Recovered || retry.MigrationBackup == info.MigrationBackup {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	defer db.Close()
	if got := fixtureRows(t, db.conn, `SELECT COUNT(*) FROM browser_profile_groups`); got != "[[5]]" {
		t.Fatalf("retry relationships=%s", got)
	}
}

func checkNoParkedDatabase(t *testing.T, path string) {
	t.Helper()
	parked, err := filepath.Glob(path + ".broken-*")
	if err != nil || len(parked) != 0 {
		t.Fatalf("unexpected .broken files: %v %v", parked, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamSchema41RefusesUnknownSchema(t *testing.T) {
	for _, tc := range []struct {
		name        string
		version     int
		upstream    bool
		breakSchema string
	}{
		{"unknown-marker", 34, true, `UPDATE schema_migrations SET desc='foreign lineage' WHERE version=34`},
		{"missing-proxy-column", 34, true, `ALTER TABLE browser_profiles DROP COLUMN proxy_bind_error`},
		{"wrong-proxy-column", 34, true, `ALTER TABLE browser_profiles DROP COLUMN proxy_bind_error; ALTER TABLE browser_profiles ADD COLUMN proxy_bind_error INTEGER`},
		{"partial-upstream", 34, true, `CREATE TABLE browser_profile_groups (unknown TEXT)`},
		{"wrong-group-index", 35, false, `DROP INDEX idx_browser_profile_groups_group; CREATE INDEX idx_browser_profile_groups_group ON browser_profile_groups(profile_id)`},
		{"wrong-sibling-expression", 35, false, `DROP INDEX idx_browser_groups_sibling_name; CREATE UNIQUE INDEX idx_browser_groups_sibling_name ON browser_groups(lower(parent_id),group_name)`},
		{"unknown-snapshot", 35, false, `ALTER TABLE browser_chatgpt_accounts RENAME COLUMN snapshot_json TO unknown`},
		{"foreign-development-marker", 36, false, `UPDATE schema_migrations SET desc='alternative migration' WHERE version=36`},
		{"middle-development-hole", 40, false, `DELETE FROM schema_migrations WHERE version=37`},
		{"wrong-earlier-marker", 40, false, `UPDATE schema_migrations SET desc='foreign' WHERE version=38`},
		{"missing-development-table", 36, false, `DROP TABLE browser_chatgpt_credentials`},
		{"wrong-development-column", 37, false, `ALTER TABLE browser_chatgpt_renewal_accounts RENAME COLUMN state TO unknown`},
		{"wrong-development-index", 38, false, `DROP INDEX browser_keypouch_profile; CREATE UNIQUE INDEX browser_keypouch_profile ON browser_keypouch_profiles(device_id,email) WHERE rejected=0`},
		{"wrong-development-index-filter", 38, false, `DROP INDEX browser_keypouch_record; CREATE UNIQUE INDEX browser_keypouch_record ON browser_keypouch_profiles(device_id,record_id) WHERE rejected=1`},
		{"missing-development-39-column", 39, false, `ALTER TABLE browser_keypouch_readonly DROP COLUMN pending_tab`},
		{"missing-development-40-column", 40, false, `ALTER TABLE browser_keypouch_template_init RENAME COLUMN singleton TO other_key`},
		{"unknown-future", 35, false, `INSERT INTO schema_migrations(version,desc) VALUES(99,'unknown fork')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed, path := makeBranchFixture(t, tc.version, tc.upstream, tc.upstream)
			instanceGroupsLegacyExec(t, seed, tc.breakSchema)
			previous := fixtureRows(t, seed.conn, fixtureHistory, 9999)
			profiles := fixtureRows(t, seed.conn, fixtureProfiles)
			extras := localFixtureTables(t, seed)
			db, info, err := OpenWithRecovery(path, filepath.Join(filepath.Dir(path), "backups"))
			if err == nil || db != nil || info.Recovered || info.MigrationBackup != "" || !strings.Contains(err.Error(), "迁移记录与实际结构不一致") {
				t.Fatalf("expected diagnostic before upgrade or recovery: %+v %v", info, err)
			}
			if err := seed.Migrate(); err == nil {
				t.Fatal("direct migration accepted the unrecognized schema")
			}
			checkNoParkedDatabase(t, path)
			if previous != fixtureRows(t, seed.conn, fixtureHistory, 9999) || profiles != fixtureRows(t, seed.conn, fixtureProfiles) || extras != localFixtureTables(t, seed) {
				t.Fatal("failed source recognition changed the database")
			}
		})
	}
	t.Run("unversioned-foreign-database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "foreign.db")
		seed, err := NewDB(path)
		if err != nil {
			t.Fatal(err)
		}
		defer seed.Close()
		instanceGroupsLegacyExec(t, seed, `CREATE TABLE unrelated(value); INSERT INTO unrelated VALUES('original')`)
		db, info, err := OpenWithRecovery(path, filepath.Join(filepath.Dir(path), "backups"))
		if err == nil || db != nil || info.Recovered || !strings.Contains(err.Error(), "缺少迁移记录") {
			t.Fatalf("open: %+v %v", info, err)
		}
		checkNoParkedDatabase(t, path)
		if got := fixtureRows(t, seed.conn, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`); got != `[["unrelated"]]` {
			t.Fatalf("foreign schema changed: %s", got)
		}
		if got := fixtureRows(t, seed.conn, `SELECT value FROM unrelated`); got != `[["original"]]` {
			t.Fatalf("foreign data changed: %s", got)
		}
	})
}

func TestUpstreamSchema41DevelopmentHistoryAfterMerge(t *testing.T) {
	seed, path := makeBranchFixture(t, 40, false, false)
	directory := filepath.Join(filepath.Dir(path), "backups")
	instanceGroupsLegacyExec(t, seed, `CREATE TABLE custom_extension (id INTEGER PRIMARY KEY, note TEXT);
		INSERT INTO custom_extension VALUES (7, 'fixture');
		ALTER TABLE browser_chatgpt_renewal ADD COLUMN extension_note TEXT;
		UPDATE browser_chatgpt_renewal SET extension_note='opaque fixture'`)
	prior := localFixtureTables(t, seed)
	for i := 0; i < 2; i++ {
		db, info, err := OpenWithRecovery(path, directory)
		if err != nil || info.Recovered {
			t.Fatalf("known development history with extension data: %+v, %v", info, err)
		}
		if fixtureRows(t, db.conn, `SELECT * FROM custom_extension`) != `[[7,"fixture"]]` || prior != localFixtureTables(t, db) {
			t.Fatal("unrelated extension tables or columns changed")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	instanceGroupsLegacyExec(t, seed, `UPDATE schema_migrations SET desc='foreign intermediate migration' WHERE version=39`)
	history := fixtureRows(t, seed.conn, fixtureHistory, 41)
	db, info, err := OpenWithRecovery(path, directory)
	if err == nil || db != nil || info.Recovered || info.MigrationBackup != "" || !strings.Contains(err.Error(), "39") {
		t.Fatalf("valid 41 hid an unknown intermediate record: %+v %v", info, err)
	}
	checkNoParkedDatabase(t, path)
	if history != fixtureRows(t, seed.conn, fixtureHistory, 41) || prior != localFixtureTables(t, seed) ||
		fixtureRows(t, seed.conn, `SELECT * FROM custom_extension`) != `[[7,"fixture"]]` {
		t.Fatal("failed validation changed original history or extension data")
	}
}

func TestUpstreamSchema41BackupFailuresKeepSource(t *testing.T) {
	t.Run("upgrade", func(t *testing.T) {
		seed, path := makeBranchFixture(t, 34, true, true)
		block := filepath.Join(filepath.Dir(path), "file-instead-of-directory")
		if err := os.WriteFile(block, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		db, info, err := OpenWithRecovery(path, block)
		if err == nil || db != nil || info.Recovered || !strings.Contains(err.Error(), "备份失败") {
			t.Fatalf("open: %+v %v", info, err)
		}
		checkNoParkedDatabase(t, path)
		if got := fixtureRows(t, seed.conn, `SELECT proxy_bind_error FROM browser_profiles WHERE profile_id='active'`); got != `[["fixture proxy unavailable"]]` {
			t.Fatalf("lost original data: %s", got)
		}
		if got := fixtureRows(t, seed.conn, `SELECT MAX(version) FROM schema_migrations`); got != "[[34]]" {
			t.Fatalf("history=%s", got)
		}
	})
	t.Run("direct-migration", func(t *testing.T) {
		seed, path := makeBranchFixture(t, 34, true, true)
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "instance-group-backups"), []byte("block"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := seed.Migrate(); err == nil {
			t.Fatal("direct migration continued without a backup")
		}
		if got := fixtureRows(t, seed.conn, `SELECT MAX(version) FROM schema_migrations`); got != "[[34]]" {
			t.Fatalf("history=%s", got)
		}
		if got := fixtureRows(t, seed.conn, `SELECT name FROM sqlite_schema WHERE name='browser_profile_groups'`); got != "null" {
			t.Fatalf("changed original: %s", got)
		}
	})
	t.Run("physical-corruption", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.db")
		original := bytes.Repeat([]byte("synthetic damage"), 256)
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		db, info, err := OpenWithRecovery(path, path)
		if err == nil || db != nil || info.Recovered || !strings.Contains(err.Error(), "备份失败") {
			t.Fatalf("open: %+v %v", info, err)
		}
		checkNoParkedDatabase(t, path)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("damaged original was not preserved: %v", err)
		}
	})
}

func TestUpstreamSchema41LockedWriterKeepsOriginal(t *testing.T) {
	seed, path := makeBranchFixture(t, 34, true, true)
	instanceGroupsLegacyExec(t, seed, `BEGIN IMMEDIATE`)
	defer seed.conn.Exec(`ROLLBACK`)
	db, info, err := OpenWithRecovery(path, filepath.Join(filepath.Dir(path), "backups"))
	if err == nil || db != nil || info.Recovered || !strings.Contains(err.Error(), "占用") {
		t.Fatalf("open: %+v %v", info, err)
	}
	checkNoParkedDatabase(t, path)
	if got := fixtureRows(t, seed.conn, `SELECT MAX(version) FROM schema_migrations`); got != "[[34]]" {
		t.Fatalf("history=%s", got)
	}
	if got := fixtureRows(t, seed.conn, `SELECT proxy_bind_error FROM browser_profiles WHERE profile_id='active'`); got != `[["fixture proxy unavailable"]]` {
		t.Fatalf("original data=%s", got)
	}
}
