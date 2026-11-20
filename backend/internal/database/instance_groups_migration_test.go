package database

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func instanceGroupsLegacyDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.GetConn().Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, desc TEXT NOT NULL DEFAULT '',
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version >= 34 {
			break
		}
		if err := db.applyMigration(migration); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}

func instanceGroupsLegacyExec(t *testing.T, db *DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.GetConn().Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func instanceGroupsLegacyGroup(t *testing.T, db *DB, id, name, parent string) {
	t.Helper()
	instanceGroupsLegacyExec(t, db, `INSERT INTO browser_groups (group_id,group_name,parent_id) VALUES (?,?,?)`, id, name, parent)
}

func instanceGroupsLegacyProfile(t *testing.T, db *DB, id, group string, tags []string, deleted bool) {
	t.Helper()
	data, err := json.Marshal(tags)
	if err != nil {
		t.Fatal(err)
	}
	deletedAt := ""
	if deleted {
		deletedAt = "2026-11-12T00:00:00Z"
	}
	instanceGroupsLegacyExec(t, db, `INSERT INTO browser_profiles
		(profile_id,profile_name,group_id,tags,created_at,updated_at,deleted_at)
		VALUES (?,?,?,?,?,?,?)`, id, id, group, string(data), "2026-11-11T00:00:00Z", "2026-11-11T00:00:00Z", deletedAt)
}

func instanceGroupsMigrationSnapshot(t *testing.T, db *DB) (map[string]string, map[string][]string) {
	t.Helper()
	groups := map[string]string{}
	rows, err := db.GetConn().Query(`SELECT group_id, group_name, COALESCE(parent_id,'') FROM browser_groups ORDER BY group_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, name, parent string
		if err := rows.Scan(&id, &name, &parent); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		groups[id] = parent + ":" + name
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	memberships := map[string][]string{}
	rows, err = db.GetConn().Query(`SELECT profile_id,group_id FROM browser_profile_groups ORDER BY profile_id,group_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var profile, group string
		if err := rows.Scan(&profile, &group); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		memberships[profile] = append(memberships[profile], group)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	return groups, memberships
}

func TestInstanceGroupsV34MigratesAllLegacyRelationshipsIdempotently(t *testing.T) {
	db, _ := instanceGroupsLegacyDB(t)
	instanceGroupsLegacyGroup(t, db, "root", "Work", "")
	instanceGroupsLegacyGroup(t, db, "child", "Child", "root")
	instanceGroupsLegacyGroup(t, db, "empty", "Empty", "")
	instanceGroupsLegacyProfile(t, db, "mixed", "child", []string{"Work", "Legacy"}, false)
	instanceGroupsLegacyProfile(t, db, "single", "root", []string{}, false)
	// A tag matching a non-root group must get its own root group.
	instanceGroupsLegacyProfile(t, db, "tags", "", []string{"Legacy", "Legacy", "Child"}, false)
	instanceGroupsLegacyProfile(t, db, "trash", "child", []string{"Trash"}, true)
	instanceGroupsLegacyProfile(t, db, "ungrouped", "", []string{}, false)
	instanceGroupsLegacyExec(t, db, `UPDATE browser_profiles SET
		fingerprint_args='["--fingerprint=fixture"]', memory_limit_mb=768,
		keywords='["keep-keyword"]', proxy_config='direct://' WHERE profile_id='mixed'`)
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	groups, memberships := instanceGroupsMigrationSnapshot(t, db)
	if len(groups) != 6 {
		t.Fatalf("groups=%d, want 6", len(groups))
	}
	if groups["root"] != ":Work" || groups["child"] != "root:Child" || groups["empty"] != ":Empty" {
		t.Fatalf("existing IDs, hierarchy or empty group changed: %v", groups)
	}
	labels := func(id string) []string {
		result := []string{}
		for _, group := range memberships[id] {
			result = append(result, groups[group])
		}
		sort.Strings(result)
		return result
	}
	want := map[string][]string{
		"mixed":     {":Legacy", ":Work", "root:Child"},
		"single":    {":Work"},
		"tags":      {":Child", ":Legacy"},
		"trash":     {":Trash", "root:Child"},
		"ungrouped": {},
	}
	for id, expected := range want {
		if actual := labels(id); !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s memberships=%v, want %v", id, actual, expected)
		}
	}
	var fingerprint, keywords, proxy string
	var memory int
	if err := db.GetConn().QueryRow(`SELECT fingerprint_args,keywords,proxy_config,memory_limit_mb FROM browser_profiles WHERE profile_id='mixed'`).Scan(&fingerprint, &keywords, &proxy, &memory); err != nil {
		t.Fatal(err)
	}
	if fingerprint != `["--fingerprint=fixture"]` || keywords != `["keep-keyword"]` || proxy != "direct://" || memory != 768 {
		t.Fatal("migration changed unrelated profile configuration")
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	groupsAgain, membershipsAgain := instanceGroupsMigrationSnapshot(t, db)
	if !reflect.DeepEqual(groups, groupsAgain) || !reflect.DeepEqual(memberships, membershipsAgain) {
		t.Fatal("second migration recreated groups or changed memberships")
	}
}

func TestInstanceGroupsV34RollsBackDDLDataAndMarkerOnFailure(t *testing.T) {
	db, _ := instanceGroupsLegacyDB(t)
	instanceGroupsLegacyGroup(t, db, "retained", "Retained", "")
	instanceGroupsLegacyProfile(t, db, "profile", "retained", []string{"CreatedFirst", "Blocked"}, false)
	instanceGroupsLegacyExec(t, db, `CREATE TRIGGER fail_instance_group_migration
		BEFORE INSERT ON browser_groups WHEN NEW.group_name='Blocked'
		BEGIN SELECT RAISE(ABORT, 'injected group migration failure'); END`)
	if err := db.Migrate(); err == nil {
		t.Fatal("fault-injected migration unexpectedly succeeded")
	}
	var version, groups, relationsTable int
	var tags, group string
	if err := db.GetConn().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.GetConn().QueryRow(`SELECT COUNT(*) FROM browser_groups`).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if err := db.GetConn().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='browser_profile_groups'`).Scan(&relationsTable); err != nil {
		t.Fatal(err)
	}
	if err := db.GetConn().QueryRow(`SELECT tags,group_id FROM browser_profiles WHERE profile_id='profile'`).Scan(&tags, &group); err != nil {
		t.Fatal(err)
	}
	if version != 33 || groups != 1 || relationsTable != 0 || tags != `["CreatedFirst","Blocked"]` || group != "retained" {
		t.Fatalf("partial migration remained: version=%d groups=%d relationTable=%d tags=%s group=%s", version, groups, relationsTable, tags, group)
	}
	instanceGroupsLegacyExec(t, db, `DROP TRIGGER fail_instance_group_migration`)
	if err := db.Migrate(); err != nil {
		t.Fatalf("retry failed after removing injected fault: %v", err)
	}
	_, memberships := instanceGroupsMigrationSnapshot(t, db)
	if len(memberships["profile"]) != 3 {
		t.Fatalf("retry did not preserve all memberships: %v", memberships)
	}
}

func TestInstanceGroupsV34RejectsInvalidLegacyDataWithoutPartialWrites(t *testing.T) {
	for _, scenario := range []string{"malformed-tags", "orphan-group", "duplicate-siblings", "parent-cycle"} {
		t.Run(scenario, func(t *testing.T) {
			db, _ := instanceGroupsLegacyDB(t)
			instanceGroupsLegacyGroup(t, db, "root", "Root", "")
			instanceGroupsLegacyProfile(t, db, "profile", "root", []string{"ValidFirst"}, false)
			switch scenario {
			case "malformed-tags":
				instanceGroupsLegacyExec(t, db, `UPDATE browser_profiles SET tags='not-json'`)
			case "orphan-group":
				instanceGroupsLegacyExec(t, db, `UPDATE browser_profiles SET group_id='missing'`)
			case "duplicate-siblings":
				instanceGroupsLegacyGroup(t, db, "duplicate", "Root", "")
			case "parent-cycle":
				instanceGroupsLegacyGroup(t, db, "child", "Child", "root")
				instanceGroupsLegacyExec(t, db, `UPDATE browser_groups SET parent_id='child' WHERE group_id='root'`)
			}
			var before int
			if err := db.GetConn().QueryRow(`SELECT COUNT(*) FROM browser_groups`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(); err == nil {
				t.Fatal("invalid legacy classification was accepted")
			}
			var after, version int
			if err := db.GetConn().QueryRow(`SELECT COUNT(*) FROM browser_groups`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if err := db.GetConn().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if before != after || version != 33 {
				t.Fatalf("invalid input partially migrated: groups %d -> %d version=%d", before, after, version)
			}
		})
	}
}

func TestInstanceGroupsV34BackupContainsUncheckpointedWALAndLegacySchema(t *testing.T) {
	db, path := instanceGroupsLegacyDB(t)
	instanceGroupsLegacyExec(t, db, `PRAGMA wal_autocheckpoint=0`)
	// Consume the checkpoint result before adding the WAL-only fixture rows.
	var busy, logPages, checkpointed int
	if err := db.GetConn().QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logPages, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatal("could not establish an empty WAL baseline")
	}
	instanceGroupsLegacyProfile(t, db, "wal-only", "", []string{"WAL fixture"}, false)
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() <= 32 {
		t.Fatalf("fixture did not produce a populated WAL: info=%v err=%v", info, err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(path), "instance-group-backups"))
	if err != nil {
		t.Fatal(err)
	}
	files := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".db") {
			files = append(files, entry.Name())
		}
	}
	if len(files) != 1 {
		t.Fatalf("expected one current migration backup, found %v", files)
	}
	backupPath := filepath.Join(filepath.Dir(path), "instance-group-backups", files[0])
	backup, err := sql.Open("sqlite", "file:"+filepath.ToSlash(backupPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var id, tags, integrity string
	var version, relationTables int
	if err := backup.QueryRow(`SELECT profile_id,tags FROM browser_profiles WHERE profile_id='wal-only'`).Scan(&id, &tags); err != nil {
		t.Fatal(err)
	}
	if err := backup.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := backup.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='browser_profile_groups'`).Scan(&relationTables); err != nil {
		t.Fatal(err)
	}
	if err := backup.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if id != "wal-only" || tags != `["WAL fixture"]` || version != 33 || relationTables != 0 || integrity != "ok" {
		t.Fatalf("backup is stale or not pre-migration: id=%s tags=%s version=%d relations=%d integrity=%s", id, tags, version, relationTables, integrity)
	}
	if info, err := os.Stat(backupPath); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("backup permissions expose private data: info=%v err=%v", info, err)
	}
	_, memberships := instanceGroupsMigrationSnapshot(t, db)
	if len(memberships["wal-only"]) != 1 {
		t.Fatal("current database did not migrate the WAL-only profile")
	}
}

func TestInstanceGroupsV34BackupFailurePreventsMigration(t *testing.T) {
	db, path := instanceGroupsLegacyDB(t)
	instanceGroupsLegacyProfile(t, db, "profile", "", []string{"Keep"}, false)
	// A regular file at the intended directory gives deterministic failure even
	// when the test runner has permission to bypass ordinary mode restrictions.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "instance-group-backups"), []byte("block directory creation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err == nil {
		t.Fatal("migration proceeded without its required backup")
	}
	var version, relations int
	if err := db.GetConn().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.GetConn().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='browser_profile_groups' AND type='table'`).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if version != 33 || relations != 0 {
		t.Fatalf("backup failure still changed schema: version=%d relations=%d", version, relations)
	}
}
