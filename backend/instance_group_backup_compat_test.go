package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceGroupBackupMergeUnionsExistingAndImportedMemberships(t *testing.T) {
	for _, identity := range []string{"same-id", "same-data-directory"} {
		t.Run(identity, func(t *testing.T) {
			target := ugCompatApp(t)
			local := ugCompatGroup(t, target, "Local only", "")
			targetParent := ugCompatGroup(t, target, "Imported parent", "")
			targetProfile := ugCompatProfile(t, target, "existing-profile", local.GroupId)
			sharedPath := filepath.Join(t.TempDir(), "shared-synthetic-data")
			targetProfile.UserDataDir = sharedPath
			if err := target.browserMgr.ProfileDAO.Upsert(targetProfile); err != nil {
				t.Fatal(err)
			}

			source := ugCompatApp(t)
			parent := ugCompatGroup(t, source, "Imported parent", "")
			leaf := ugCompatGroup(t, source, "Imported leaf", parent.GroupId)
			other := ugCompatGroup(t, source, "Imported other", "")
			ugCompatGroup(t, source, "Imported empty", "")
			sourceID := "existing-profile"
			if identity == "same-data-directory" {
				sourceID = "source-existing-profile"
			}
			incoming := ugCompatProfile(t, source, sourceID, leaf.GroupId, other.GroupId)
			incoming.UserDataDir = sharedPath
			if err := source.browserMgr.ProfileDAO.Upsert(incoming); err != nil {
				t.Fatal(err)
			}
			newProfile := ugCompatProfile(t, source, "new-imported-profile", leaf.GroupId, other.GroupId)
			ugCompatInsertGroup(t, target, leaf.GroupId, "Local colliding ID", "")
			sourcePath := filepath.Join(source.appRoot, "data", "app.db")
			if err := source.db.Close(); err != nil {
				t.Fatal(err)
			}

			var firstGroupCount int
			for attempt := 0; attempt < 2; attempt++ {
				mappings, err := target.backupMergeDatabaseFromSource(sourcePath, nil, &backupMergeStats{})
				if err != nil {
					t.Fatal(err)
				}
				if mappings == nil || backupImportMappedID(mappings.Profiles, sourceID) != targetProfile.ProfileId {
					t.Fatalf("source identity was not mapped onto the existing profile: %+v", mappings)
				}
				newID := backupImportMappedID(mappings.Profiles, newProfile.ProfileId)
				if newID == "" || newID == targetProfile.ProfileId {
					t.Fatal("new source profile did not receive a distinct mapping")
				}
				importedLeaf := ugCompatFindGroup(t, target, "Imported leaf", targetParent.GroupId)
				importedOther := ugCompatFindGroup(t, target, "Imported other", "")
				if importedLeaf.GroupId == leaf.GroupId {
					t.Fatal("backup merge reused a colliding group ID")
				}
				ugCompatStoredGroups(t, target, targetProfile.ProfileId, local.GroupId, importedLeaf.GroupId, importedOther.GroupId)
				ugCompatStoredGroups(t, target, newID, importedLeaf.GroupId, importedOther.GroupId)
				ugCompatFindGroup(t, target, "Imported empty", "")
				if ugCompatCount(t, target, `SELECT COUNT(*) FROM browser_profiles`) != 2 || ugCompatCount(t, target, `SELECT COUNT(*) FROM browser_profile_groups`) != 5 {
					t.Fatal("merge duplicated profiles or lost/duplicated memberships")
				}
				groupCount := ugCompatCount(t, target, `SELECT COUNT(*) FROM browser_groups`)
				if attempt == 0 {
					firstGroupCount = groupCount
				} else if groupCount != firstGroupCount {
					t.Fatal("repeating the same merge created duplicate groups")
				}
				ugCompatForeignKeys(t, target)
			}
		})
	}
}

func TestInstanceGroupBackupMergeReadsLegacyColumnsAndTrash(t *testing.T) {
	source := ugCompatApp(t)
	parent := ugCompatGroup(t, source, "Legacy root", "")
	leaf := ugCompatGroup(t, source, "Legacy child", parent.GroupId)
	ugCompatGroup(t, source, "Legacy empty", "")
	// Reconstruct the pre-migration classification schema, leaving all other
	// columns intact. The archive import must adapt it without mutating source.
	for _, statement := range []string{
		`DROP TABLE browser_profile_groups`,
		`DROP INDEX idx_browser_groups_sibling_name`,
		`DELETE FROM schema_migrations WHERE version >= 34`,
	} {
		if _, err := source.db.GetConn().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ id, deletedAt string }{
		{"legacy-active", ""},
		{"legacy-trash", "2026-11-11T00:00:00Z"},
	} {
		if _, err := source.db.GetConn().Exec(`INSERT INTO browser_profiles
(profile_id,profile_name,user_data_dir,tags,group_id,created_at,updated_at,deleted_at)
VALUES (?,?,?,'["Legacy root","Legacy tag"]',?,'2026-11-10T00:00:00Z','2026-11-10T00:00:00Z',?)`, item.id, item.id, item.id, leaf.GroupId, item.deletedAt); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(source.appRoot, "data", "app.db")
	if err := source.db.Close(); err != nil {
		t.Fatal(err)
	}
	target := ugCompatApp(t)
	mappings, err := target.backupMergeDatabaseFromSource(sourcePath, nil, &backupMergeStats{})
	if err != nil {
		t.Fatal(err)
	}
	if mappings == nil {
		t.Fatal("legacy merge returned no reference mappings")
	}
	importedParent := ugCompatFindGroup(t, target, "Legacy root", "")
	importedLeaf := ugCompatFindGroup(t, target, "Legacy child", importedParent.GroupId)
	importedTag := ugCompatFindGroup(t, target, "Legacy tag", "")
	ugCompatFindGroup(t, target, "Legacy empty", "")
	for _, oldID := range []string{"legacy-active", "legacy-trash"} {
		id := backupImportMappedID(mappings.Profiles, oldID)
		if id == "" {
			t.Fatalf("legacy profile %s was not mapped", oldID)
		}
		profile := ugCompatStoredGroups(t, target, id, importedParent.GroupId, importedLeaf.GroupId, importedTag.GroupId)
		if (profile.DeletedAt != "") != (oldID == "legacy-trash") {
			t.Fatalf("recycle-bin status changed for %s", oldID)
		}
	}
	if ugCompatCount(t, target, `SELECT COUNT(*) FROM browser_groups`) != 4 {
		t.Fatal("legacy merge failed to deduplicate a tag matching an existing root or lost an empty group")
	}
	ugCompatForeignKeys(t, target)
}

func TestInstanceGroupBackupRollbackRestoresCascadeDeletedRelations(t *testing.T) {
	app := ugCompatApp(t)
	parent := ugCompatGroup(t, app, "Rollback parent", "")
	leaf := ugCompatGroup(t, app, "Rollback leaf", parent.GroupId)
	other := ugCompatGroup(t, app, "Rollback other", "")
	empty := ugCompatGroup(t, app, "Rollback empty", "")
	ugCompatProfile(t, app, "rollback-active", leaf.GroupId, other.GroupId)
	ugCompatProfile(t, app, "rollback-trash", other.GroupId)
	if err := app.browserMgr.ProfileDAO.SoftDelete("rollback-trash", "2026-11-11T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	ugCompatForeignKeys(t, app)
	if err := app.config.Save(filepath.Join(app.appRoot, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	rollback, err := app.beginBackupImportRollback()
	if err != nil {
		t.Fatal(err)
	}
	defer rollback.cleanup()
	if info, err := os.Stat(rollback.dbSnapshotPath); err != nil || info.Size() == 0 {
		t.Fatalf("consistent rollback snapshot was not produced: %v", err)
	}
	if _, err := app.db.GetConn().Exec(`DELETE FROM browser_profiles WHERE profile_id='rollback-active'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.GetConn().Exec(`DELETE FROM browser_groups WHERE group_id=?`, other.GroupId); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.GetConn().Exec(`UPDATE browser_groups SET parent_id='',group_name='Mutated leaf' WHERE group_id=?`, leaf.GroupId); err != nil {
		t.Fatal(err)
	}
	extra := ugCompatGroup(t, app, "After snapshot", "")
	ugCompatProfile(t, app, "after-snapshot-profile", extra.GroupId)
	if ugCompatCount(t, app, `SELECT COUNT(*) FROM browser_profile_groups WHERE profile_id IN ('rollback-active','rollback-trash')`) != 0 {
		t.Fatal("fault setup did not actually cascade-delete the original relations")
	}
	if err := restoreBackupImportDatabase(app, rollback.dbSnapshotPath); err != nil {
		t.Fatal(err)
	}
	active := ugCompatStoredGroups(t, app, "rollback-active", leaf.GroupId, other.GroupId)
	trash := ugCompatStoredGroups(t, app, "rollback-trash", other.GroupId)
	if active.DeletedAt != "" || trash.DeletedAt != "2026-11-11T00:00:00Z" {
		t.Fatal("rollback changed active/trash state")
	}
	if ugCompatFindGroup(t, app, "Rollback leaf", parent.GroupId).GroupId != leaf.GroupId || ugCompatFindGroup(t, app, "Rollback empty", "").GroupId != empty.GroupId {
		t.Fatal("rollback lost group identities or hierarchy")
	}
	if ugCompatCount(t, app, `SELECT COUNT(*) FROM browser_profiles`) != 2 || ugCompatCount(t, app, `SELECT COUNT(*) FROM browser_groups`) != 4 || ugCompatCount(t, app, `SELECT COUNT(*) FROM browser_profile_groups`) != 3 {
		t.Fatal("rollback left imported rows or lost original membership rows")
	}
	ugCompatForeignKeys(t, app)
}
