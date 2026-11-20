package backend

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"ant-chrome/backend/internal/browser"
)

// All import failure fixtures use isolated synthetic data.
func ugImportGuardSnapshot(t *testing.T, app *App) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, query := range []string{
		`SELECT * FROM browser_groups ORDER BY group_id`,
		`SELECT * FROM browser_profiles ORDER BY profile_id`,
		`SELECT * FROM browser_profile_groups ORDER BY profile_id,group_id`,
		`SELECT * FROM browser_bookmarks ORDER BY id`,
	} {
		rows, err := app.db.GetConn().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		all := [][]any{}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if bytes, ok := value.([]byte); ok {
					values[i] = string(bytes)
				}
			}
			all = append(all, values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(all)
		if err != nil {
			t.Fatal(err)
		}
		result[query] = string(encoded)
	}
	return result
}

func ugImportGuardUnchanged(t *testing.T, app *App, before map[string]string) {
	t.Helper()
	if after := ugImportGuardSnapshot(t, app); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected import changed persisted groups, profiles, memberships, or bookmarks")
	}
	ugCompatForeignKeys(t, app)
}

func ugImportGuardPackage(t *testing.T, app *App, groups []ProfilePackageDatabaseGroup, groupIDs []string, replacedID string) error {
	t.Helper()
	source := browser.Profile{ProfileId: "source-package-profile", ProfileName: "Synthetic incoming profile", UserDataDir: "source-package-profile", GroupIds: groupIDs}
	target := source
	target.ProfileId = "new-package-profile"
	target.UserDataDir = "new-package-profile"
	snapshot := ProfilePackageDatabase{
		Format: profilePackageDatabaseFormat, Version: profilePackageDatabaseVersion,
		Profiles: []browser.Profile{source}, Groups: groups,
	}
	prepared := []preparedProfilePackageImport{{
		OldProfileID: source.ProfileId, Profile: target,
		ReplacedProfileID: replacedID, Overwrite: replacedID != "",
	}}
	return app.restoreProfilePackageDatabase(snapshot, prepared, nil)
}

func TestInstanceGroupPackageRejectsAmbiguousSourceCatalogAtomically(t *testing.T) {
	for _, scenario := range []string{"duplicate-root-names", "duplicate-child-names", "case-colliding-ids", "missing-parent-matches-target", "wrong-case-parent", "wrong-case-membership", "missing-membership-matches-target"} {
		t.Run(scenario, func(t *testing.T) {
			app := ugCompatApp(t)
			ugCompatInsertGroup(t, app, "local-parent", "Local parent", "")
			ugCompatProfile(t, app, "kept-profile", "local-parent")
			groups := []ProfilePackageDatabaseGroup{{GroupId: "source-parent", GroupName: "Source parent"}}
			ids := []string{"source-one", "source-two"}
			switch scenario {
			case "duplicate-root-names":
				groups = append(groups, ProfilePackageDatabaseGroup{GroupId: ids[0], GroupName: "Duplicate root"}, ProfilePackageDatabaseGroup{GroupId: ids[1], GroupName: "Duplicate root"})
			case "duplicate-child-names":
				groups = append(groups, ProfilePackageDatabaseGroup{GroupId: ids[0], GroupName: "Duplicate child", ParentId: "source-parent"}, ProfilePackageDatabaseGroup{GroupId: ids[1], GroupName: "Duplicate child", ParentId: "source-parent"})
			case "case-colliding-ids":
				ids = []string{"Source-ID", "source-id"}
				groups = append(groups, ProfilePackageDatabaseGroup{GroupId: ids[0], GroupName: "Upper source"}, ProfilePackageDatabaseGroup{GroupId: ids[1], GroupName: "Lower source"})
			case "missing-parent-matches-target":
				ids = []string{"source-child"}
				groups = append(groups, ProfilePackageDatabaseGroup{GroupId: ids[0], GroupName: "Source child", ParentId: "local-parent"})
			case "wrong-case-parent":
				ids = []string{"source-child"}
				groups = append(groups, ProfilePackageDatabaseGroup{GroupId: ids[0], GroupName: "Source child", ParentId: "SOURCE-PARENT"})
			case "wrong-case-membership":
				ids = []string{"SOURCE-PARENT"}
			case "missing-membership-matches-target":
				ids = []string{"local-parent"}
			}
			before := ugImportGuardSnapshot(t, app)
			// The package transaction first soft-deletes kept-profile for an
			// overwrite, so refusal must restore more than the group count.
			if err := ugImportGuardPackage(t, app, groups, ids, "kept-profile"); err == nil {
				t.Error("ambiguous or incomplete source group catalog was accepted")
			}
			ugImportGuardUnchanged(t, app, before)
		})
	}
}

func TestInstanceGroupBackupRejectsDuplicateSiblingNamesAtomically(t *testing.T) {
	for _, parentID := range []string{"", "source-parent"} {
		t.Run("parent="+parentID, func(t *testing.T) {
			target := ugCompatApp(t)
			ugCompatInsertGroup(t, target, "keep-group", "Local keep", "")
			ugCompatProfile(t, target, "keep-profile", "keep-group")
			source := ugCompatApp(t)
			// Pre-v34 catalogues permitted this, so source validation cannot
			// rely on the target's unique index or infer the two IDs are one.
			for _, statement := range []string{
				`DROP TABLE browser_profile_groups`,
				`DROP INDEX idx_browser_groups_sibling_name`,
				`DELETE FROM schema_migrations WHERE version>=34`,
			} {
				if _, err := source.db.GetConn().Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			ugCompatInsertGroup(t, source, "source-parent", "Source parent", "")
			ugCompatInsertGroup(t, source, "source-one", "Duplicate sibling", parentID)
			ugCompatInsertGroup(t, source, "source-two", "Duplicate sibling", parentID)
			if _, err := source.db.GetConn().Exec(`INSERT INTO browser_profiles (profile_id,profile_name,user_data_dir,group_id,tags,created_at,updated_at) VALUES ('incoming-profile','Incoming profile','incoming-profile','source-two','[]','2026-11-12T00:00:00Z','2026-11-12T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			if _, err := source.db.GetConn().Exec(`INSERT INTO browser_bookmarks (name,url) VALUES ('Rollback marker','https://synthetic.invalid/duplicate-rollback')`); err != nil {
				t.Fatal(err)
			}
			sourcePath := filepath.Join(source.appRoot, "data", "app.db")
			if err := source.db.Close(); err != nil {
				t.Fatal(err)
			}
			before := ugImportGuardSnapshot(t, target)
			if _, err := target.backupMergeDatabaseFromSource(sourcePath, nil, &backupMergeStats{}); err == nil {
				t.Error("two distinct same-name sibling source groups were silently merged")
			}
			ugImportGuardUnchanged(t, target, before)
		})
	}
}

func TestInstanceGroupBackupRejectsMissingSourceReferencesDespiteTargetIDs(t *testing.T) {
	for _, scenario := range []string{"missing-group", "missing-profile", "missing-both", "missing-parent", "legacy-missing-group", "wrong-case-group", "wrong-case-profile", "wrong-case-parent", "missing-profile-table"} {
		t.Run(scenario, func(t *testing.T) {
			target := ugCompatApp(t)
			ugCompatInsertGroup(t, target, "coincident-group", "Local coincident group", "")
			ugCompatProfile(t, target, "coincident-profile", "coincident-group")
			source := ugCompatApp(t)
			ugCompatInsertGroup(t, source, "source-group", "Valid source group", "")
			ugCompatProfile(t, source, "source-profile", "source-group")
			if _, err := source.db.GetConn().Exec(`INSERT INTO browser_bookmarks (name,url) VALUES ('Rollback marker','https://synthetic.invalid/reference-rollback')`); err != nil {
				t.Fatal(err)
			}
			if _, err := source.db.GetConn().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			var statement string
			switch scenario {
			case "missing-group":
				statement = `INSERT INTO browser_profile_groups (profile_id,group_id) VALUES ('source-profile','coincident-group')`
			case "missing-profile":
				statement = `INSERT INTO browser_profile_groups (profile_id,group_id) VALUES ('coincident-profile','source-group')`
			case "missing-both":
				statement = `INSERT INTO browser_profile_groups (profile_id,group_id) VALUES ('coincident-profile','coincident-group')`
			case "missing-parent":
				statement = `UPDATE browser_groups SET parent_id='coincident-group' WHERE group_id='source-group'`
			case "legacy-missing-group":
				if _, err := source.db.GetConn().Exec(`DROP TABLE browser_profile_groups`); err != nil {
					t.Fatal(err)
				}
				statement = `UPDATE browser_profiles SET group_id='coincident-group' WHERE profile_id='source-profile'`
			case "wrong-case-group":
				statement = `INSERT INTO browser_profile_groups (profile_id,group_id) VALUES ('source-profile','SOURCE-GROUP')`
			case "wrong-case-profile":
				statement = `INSERT INTO browser_profile_groups (profile_id,group_id) VALUES ('SOURCE-PROFILE','source-group')`
			case "wrong-case-parent":
				ugCompatInsertGroup(t, source, "source-parent", "Source parent", "")
				statement = `UPDATE browser_groups SET parent_id='SOURCE-PARENT' WHERE group_id='source-group'`
			case "missing-profile-table":
				statement = `DROP TABLE browser_profiles`
			}
			if _, err := source.db.GetConn().Exec(statement); err != nil {
				t.Fatal(err)
			}
			sourcePath := filepath.Join(source.appRoot, "data", "app.db")
			if err := source.db.Close(); err != nil {
				t.Fatal(err)
			}
			before := ugImportGuardSnapshot(t, target)
			if _, err := target.backupMergeDatabaseFromSource(sourcePath, nil, &backupMergeStats{}); err == nil {
				t.Error("invalid source reference was repaired using a destination or normalized ID")
			}
			ugImportGuardUnchanged(t, target, before)
		})
	}
}

func TestInstanceGroupPackageDoesNotReuseChildUnderCaseDistinctParent(t *testing.T) {
	app := ugCompatApp(t)
	ugCompatInsertGroup(t, app, "G", "Upper root", "")
	ugCompatInsertGroup(t, app, "g", "Lower root", "")
	ugCompatInsertGroup(t, app, "child", "Child", "g")
	ugCompatProfile(t, app, "kept-profile", "G", "g", "child")
	before := ugImportGuardSnapshot(t, app)
	groups := []ProfilePackageDatabaseGroup{
		{GroupId: "G", GroupName: "Upper root"},
		{GroupId: "child", GroupName: "Child", ParentId: "G"},
	}
	err := ugImportGuardPackage(t, app, groups, []string{"child"}, "")
	if err != nil {
		ugImportGuardUnchanged(t, app, before)
		return
	}
	// Exact-ID support is also acceptable, provided source membership really
	// lands under G and the existing lower-root child remains unchanged.
	var parent string
	if err := app.db.GetConn().QueryRow(`SELECT g.parent_id FROM browser_profile_groups m JOIN browser_groups g ON g.group_id=m.group_id WHERE m.profile_id='new-package-profile'`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != "G" {
		t.Fatalf("source child under G was silently attached under %q", parent)
	}
	if err := app.db.GetConn().QueryRow(`SELECT parent_id FROM browser_groups WHERE group_id='child'`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != "g" {
		t.Fatalf("existing child was moved from g to %q", parent)
	}
	ugCompatStoredGroups(t, app, "kept-profile", "G", "g", "child")
}

func TestInstanceGroupPackageExportNeverDropsCaseDistinctGroupID(t *testing.T) {
	app := ugCompatApp(t)
	ugCompatInsertGroup(t, app, "G", "Upper root", "")
	ugCompatInsertGroup(t, app, "g", "Lower root", "")
	profile := ugCompatProfile(t, app, "export-profile", "G", "g")
	snapshot, err := app.collectProfilePackageDatabase([]browser.Profile{*profile})
	if err != nil {
		return // Explicitly refusing ambiguous IDs is preferable to data loss.
	}
	seen := map[string]bool{}
	for _, group := range snapshot.Groups {
		seen[group.GroupId] = true
	}
	if !seen["G"] || !seen["g"] {
		t.Fatal("successful export silently omitted a case-distinct group")
	}
}

func TestInstanceGroupBackupDoesNotAttachToCaseDistinctTargetProfile(t *testing.T) {
	target := ugCompatApp(t)
	ugCompatInsertGroup(t, target, "upper-only", "Upper profile group", "")
	ugCompatInsertGroup(t, target, "lower-only", "Lower profile group", "")
	ugCompatProfile(t, target, "P", "upper-only")
	ugCompatProfile(t, target, "p", "lower-only")
	source := ugCompatApp(t)
	ugCompatInsertGroup(t, source, "incoming-only", "Incoming source group", "")
	ugCompatProfile(t, source, "P", "incoming-only")
	if _, err := source.db.GetConn().Exec(`UPDATE browser_profiles SET user_data_dir='' WHERE profile_id='P'`); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(source.appRoot, "data", "app.db")
	if err := source.db.Close(); err != nil {
		t.Fatal(err)
	}
	before := ugImportGuardSnapshot(t, target)
	mappings, err := target.backupMergeDatabaseFromSource(sourcePath, nil, &backupMergeStats{})
	if err != nil {
		ugImportGuardUnchanged(t, target, before)
		return
	}
	if actual := backupImportMappedID(mappings.Profiles, "P"); actual != "P" {
		t.Fatalf("source P was silently mapped to case-distinct destination %q", actual)
	}
	ugCompatStoredGroups(t, target, "P", "upper-only", "incoming-only")
	ugCompatStoredGroups(t, target, "p", "lower-only")
}
