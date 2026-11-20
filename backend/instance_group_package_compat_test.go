package backend

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"ant-chrome/backend/internal/browser"
)

func TestInstanceGroupPackageRoundTripAllMembershipsAndAncestors(t *testing.T) {
	source := ugCompatApp(t)
	parent := ugCompatGroup(t, source, "Package parent", "")
	leaf := ugCompatGroup(t, source, "Package leaf", parent.GroupId)
	other := ugCompatGroup(t, source, "Package other", "")
	ugCompatGroup(t, source, "Unrelated empty group", "")
	profile := ugCompatProfile(t, source, "package-profile", leaf.GroupId, other.GroupId)
	dataDir := filepath.Join(source.config.Browser.UserDataRoot, profile.UserDataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "Preferences"), []byte(`{"synthetic":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(source.appRoot, "multi-group.zip")
	if count, err := source.writeProfilePackage(zipPath, []browser.Profile{*profile}); err != nil || count == 0 {
		t.Fatalf("export package: files=%d err=%v", count, err)
	}
	contents, err := openProfilePackageContents(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if contents.DatabaseSnapshot == nil || len(contents.Profiles) != 1 || len(contents.DatabaseSnapshot.Groups) != 3 {
		contents.Reader.Close()
		t.Fatal("package must contain one profile, both memberships, and the ancestor, without unrelated empty groups")
	}
	ugCompatIDs(t, "package JSON", contents.Profiles[0].GroupIds, []string{leaf.GroupId, other.GroupId})
	if err := contents.Reader.Close(); err != nil {
		t.Fatal(err)
	}

	target := ugCompatApp(t)
	targetParent := ugCompatGroup(t, target, "Package parent", "")
	// The parent can be reused by sibling name, while this leaf ID is already
	// owned by a different group and therefore has to be remapped.
	ugCompatInsertGroup(t, target, leaf.GroupId, "Existing ID collision", "")
	result, err := target.importProfilePackageFromPath(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	newID := result.ProfileMappings[profile.ProfileId]
	if result.ImportedCount != 1 || newID == "" || newID == profile.ProfileId {
		t.Fatalf("import did not create and map exactly one profile: %+v", result)
	}
	importedLeaf := ugCompatFindGroup(t, target, "Package leaf", targetParent.GroupId)
	importedOther := ugCompatFindGroup(t, target, "Package other", "")
	if importedLeaf.GroupId == leaf.GroupId {
		t.Fatal("colliding group ID was reused for a different group")
	}
	if ugCompatFindGroup(t, target, "Existing ID collision", "").GroupId != leaf.GroupId {
		t.Fatal("import changed the preexisting group's identity")
	}
	ugCompatStoredGroups(t, target, newID, importedLeaf.GroupId, importedOther.GroupId)
	ugCompatMemoryGroups(t, target, newID, importedLeaf.GroupId, importedOther.GroupId)
	if ugCompatCount(t, target, `SELECT COUNT(*) FROM browser_groups WHERE group_name='Unrelated empty group'`) != 0 {
		t.Fatal("selected-instance package imported an unrelated empty group")
	}
	if contents, err := os.ReadFile(filepath.Join(target.config.Browser.UserDataRoot, newID, "Preferences")); err != nil || string(contents) != `{"synthetic":true}` {
		t.Fatalf("selected user data was not preserved: err=%v", err)
	}
	ugCompatForeignKeys(t, target)
}

func TestInstanceGroupPackageReadsLegacyFormatsWithoutDroppingTags(t *testing.T) {
	for _, mode := range []string{"v1-tags-only", "v1-group-and-tags", "v2-database-v1"} {
		t.Run(mode, func(t *testing.T) {
			app := ugCompatApp(t)
			profile := map[string]any{
				"profileId": "legacy-package-profile", "profileName": "Legacy synthetic profile",
				"userDataDir": "legacy-package-profile", "tags": []string{"Legacy A", "Legacy B"},
				"keywords": []string{"keep-keyword"},
			}
			var existingGroupID string
			var packageGroups []ProfilePackageDatabaseGroup
			if mode == "v1-group-and-tags" {
				// v1 has no group metadata, so a supplied ID is only meaningful
				// when that group can be resolved in the receiving database.
				existingGroupID = ugCompatGroup(t, app, "Legacy known", "").GroupId
				profile["groupId"] = existingGroupID
			}
			if mode == "v2-database-v1" {
				profile["groupId"] = "legacy-leaf"
				packageGroups = []ProfilePackageDatabaseGroup{
					{GroupId: "legacy-parent", GroupName: "Legacy parent"},
					{GroupId: "legacy-leaf", GroupName: "Legacy leaf", ParentId: "legacy-parent"},
				}
			}
			zipPath := filepath.Join(app.appRoot, mode+".zip")
			file, err := os.Create(zipPath)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			// Maps deliberately omit groupIds: marshaling the new Profile
			// type would not faithfully reproduce an old producer's JSON.
			if mode == "v2-database-v1" {
				writeJSONToZip(t, writer, "manifest.json", ProfilePackageManifest{Format: profilePackageFormat, Version: 2, DatabaseVersion: 1, ProfileCount: 1})
				writeJSONToZip(t, writer, "database.json", map[string]any{
					"format": profilePackageDatabaseFormat, "version": 1,
					"profiles": []map[string]any{profile}, "groups": packageGroups,
				})
			} else {
				writeJSONToZip(t, writer, "manifest.json", ProfilePackageManifest{Format: profilePackageFormat, Version: 1, ProfileCount: 1})
				writeJSONToZip(t, writer, "profiles.json", []map[string]any{profile})
			}
			if err := writer.Close(); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			result, err := app.importProfilePackageFromPath(zipPath)
			if err != nil {
				t.Fatal(err)
			}
			newID := result.ProfileMappings["legacy-package-profile"]
			if result.ImportedCount != 1 || newID == "" {
				t.Fatalf("legacy package was not imported: %+v", result)
			}
			want := []string{
				ugCompatFindGroup(t, app, "Legacy A", "").GroupId,
				ugCompatFindGroup(t, app, "Legacy B", "").GroupId,
			}
			if existingGroupID != "" {
				want = append(want, existingGroupID)
			}
			if mode == "v2-database-v1" {
				parent := ugCompatFindGroup(t, app, "Legacy parent", "")
				want = append(want, ugCompatFindGroup(t, app, "Legacy leaf", parent.GroupId).GroupId)
			}
			ugCompatStoredGroups(t, app, newID, want...)
			ugCompatMemoryGroups(t, app, newID, want...)
			ugCompatForeignKeys(t, app)
		})
	}
}

func TestInstanceGroupPackageOverwritePreservesTrashedMemberships(t *testing.T) {
	source := ugCompatApp(t)
	importGroup := ugCompatGroup(t, source, "Imported group", "")
	profile := ugCompatProfile(t, source, "overwrite-profile", importGroup.GroupId)
	zipPath := filepath.Join(source.appRoot, "overwrite-groups.zip")
	if _, err := source.writeProfilePackage(zipPath, []browser.Profile{*profile}); err != nil {
		t.Fatal(err)
	}
	target := ugCompatApp(t)
	a := ugCompatGroup(t, target, "Original A", "")
	b := ugCompatGroup(t, target, "Original B", "")
	ugCompatProfile(t, target, profile.ProfileId, a.GroupId, b.GroupId)
	result, err := target.importProfilePackageFromPathWithMode(zipPath, profilePackageImportModeOverwrite)
	if err != nil {
		t.Fatal(err)
	}
	newID := result.ProfileMappings[profile.ProfileId]
	if result.OverwrittenCount != 1 || newID == "" || newID == profile.ProfileId {
		t.Fatalf("overwrite did not preserve a distinct original: %+v", result)
	}
	original := ugCompatStoredGroups(t, target, profile.ProfileId, a.GroupId, b.GroupId)
	if original.DeletedAt == "" {
		t.Fatal("overwritten profile was not moved to trash")
	}
	importedGroup := ugCompatFindGroup(t, target, "Imported group", "")
	imported := ugCompatStoredGroups(t, target, newID, importedGroup.GroupId)
	if imported.DeletedAt != "" {
		t.Fatal("new imported profile is unexpectedly trashed")
	}
	ugCompatMemoryGroups(t, target, newID, importedGroup.GroupId)
	ugCompatForeignKeys(t, target)
}
