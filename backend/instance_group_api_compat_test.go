package backend

import (
	"slices"
	"strings"
	"testing"
)

func TestInstanceGroupAPIChangesPreserveOtherMemberships(t *testing.T) {
	app := ugCompatApp(t)
	a := ugCompatGroup(t, app, "Group A", "")
	b := ugCompatGroup(t, app, "Group B", "")
	c := ugCompatGroup(t, app, "Group C", "")
	ugCompatProfile(t, app, "profile-a", a.GroupId, b.GroupId)
	ugCompatProfile(t, app, "profile-b", b.GroupId)
	ugCompatProfile(t, app, "profile-c")

	for attempt := 0; attempt < 2; attempt++ {
		if err := app.BrowserProfileChangeGroups([]string{"profile-a", "profile-b", "profile-a"}, []string{c.GroupId, c.GroupId}, "add"); err != nil {
			t.Fatal(err)
		}
	}
	ugCompatStoredGroups(t, app, "profile-a", a.GroupId, b.GroupId, c.GroupId)
	ugCompatStoredGroups(t, app, "profile-b", b.GroupId, c.GroupId)
	ugCompatMemoryGroups(t, app, "profile-a", a.GroupId, b.GroupId, c.GroupId)
	ugCompatMemoryGroups(t, app, "profile-b", b.GroupId, c.GroupId)

	for attempt := 0; attempt < 2; attempt++ {
		if err := app.BrowserProfileChangeGroups([]string{"profile-a", "profile-b"}, []string{b.GroupId}, "remove"); err != nil {
			t.Fatal(err)
		}
	}
	ugCompatStoredGroups(t, app, "profile-a", a.GroupId, c.GroupId)
	ugCompatStoredGroups(t, app, "profile-b", c.GroupId)
	ugCompatMemoryGroups(t, app, "profile-a", a.GroupId, c.GroupId)

	if err := app.BrowserProfileChangeGroups([]string{"profile-c"}, []string{b.GroupId, c.GroupId}, "set"); err != nil {
		t.Fatal(err)
	}
	if err := app.BrowserProfileChangeGroups([]string{"profile-a"}, []string{}, "set"); err != nil {
		t.Fatal(err)
	}
	profile := ugCompatStoredGroups(t, app, "profile-a")
	if !slices.Equal(profile.Keywords, []string{"keep-keyword"}) {
		t.Fatal("group write changed unrelated profile keywords")
	}
	ugCompatStoredGroups(t, app, "profile-b", c.GroupId)
	ugCompatStoredGroups(t, app, "profile-c", b.GroupId, c.GroupId)
	ugCompatMemoryGroups(t, app, "profile-a")
	ugCompatMemoryGroups(t, app, "profile-c", b.GroupId, c.GroupId)
	ugCompatForeignKeys(t, app)
}

func TestInstanceGroupAPIBatchRollsBackOnWriteFailure(t *testing.T) {
	app := ugCompatApp(t)
	a := ugCompatGroup(t, app, "Original", "")
	b := ugCompatGroup(t, app, "Added", "")
	ugCompatProfile(t, app, "batch-a", a.GroupId)
	ugCompatProfile(t, app, "batch-b", a.GroupId)
	// A real SQLite write failure on the second member exercises rollback after
	// at least one earlier member could have been changed by the same batch.
	if _, err := app.db.GetConn().Exec(`CREATE TEMP TRIGGER ug_compat_reject_membership
BEFORE INSERT ON browser_profile_groups WHEN NEW.profile_id='batch-b'
BEGIN SELECT RAISE(ABORT, 'injected group write failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := app.BrowserProfileChangeGroups([]string{"batch-a", "batch-b"}, []string{b.GroupId}, "add")
	if err == nil || !strings.Contains(err.Error(), "injected group write failure") {
		t.Fatalf("expected the injected SQLite failure, got %v", err)
	}
	for _, id := range []string{"batch-a", "batch-b"} {
		ugCompatStoredGroups(t, app, id, a.GroupId)
		ugCompatMemoryGroups(t, app, id, a.GroupId)
	}
	if _, err := app.db.GetConn().Exec(`DROP TRIGGER ug_compat_reject_membership`); err != nil {
		t.Fatal(err)
	}
	// Positive calibration: the exact operation succeeds with the fault removed.
	if err := app.BrowserProfileChangeGroups([]string{"batch-a", "batch-b"}, []string{b.GroupId}, "add"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"batch-a", "batch-b"} {
		ugCompatStoredGroups(t, app, id, a.GroupId, b.GroupId)
		ugCompatMemoryGroups(t, app, id, a.GroupId, b.GroupId)
	}
}

func TestLegacyTagsAdaptWithoutReplacingMemberships(t *testing.T) {
	app := ugCompatApp(t)
	a := ugCompatGroup(t, app, "Keep A", "")
	b := ugCompatGroup(t, app, "Keep B", "")
	ugCompatProfile(t, app, "legacy-profile", a.GroupId, b.GroupId)
	if err := app.BrowserProfileBatchSetTags([]string{"legacy-profile"}, []string{"Would replace"}, true); err == nil {
		t.Fatal("legacy replace must reject instead of discarding additional memberships")
	}
	ugCompatStoredGroups(t, app, "legacy-profile", a.GroupId, b.GroupId)
	ugCompatMemoryGroups(t, app, "legacy-profile", a.GroupId, b.GroupId)
	if ugCompatCount(t, app, `SELECT COUNT(*) FROM browser_groups WHERE group_name='Would replace'`) != 0 {
		t.Fatal("rejected replacement created a group")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := app.BrowserProfileBatchSetTags([]string{"legacy-profile"}, []string{"Keep A", "Legacy addition"}, false); err != nil {
			t.Fatal(err)
		}
	}
	added := ugCompatFindGroup(t, app, "Legacy addition", "")
	ugCompatStoredGroups(t, app, "legacy-profile", a.GroupId, b.GroupId, added.GroupId)
	ugCompatMemoryGroups(t, app, "legacy-profile", a.GroupId, b.GroupId, added.GroupId)
	if err := app.BrowserRenameTag("Legacy addition", "Renamed addition"); err != nil {
		t.Fatal(err)
	}
	if renamed := ugCompatFindGroup(t, app, "Renamed addition", ""); renamed.GroupId != added.GroupId {
		t.Fatal("legacy rename changed group identity")
	}
	renamedProfile := ugCompatStoredGroups(t, app, "legacy-profile", a.GroupId, b.GroupId, added.GroupId)
	if !slices.Contains(renamedProfile.Tags, "Renamed addition") || slices.Contains(renamedProfile.Tags, "Legacy addition") {
		t.Fatal("legacy read projection did not follow the group rename")
	}
	matches, err := app.BrowserProfileListByTag("Renamed addition")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ProfileId != "legacy-profile" {
		t.Fatal("legacy tag filter did not follow the group rename")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := app.BrowserProfileBatchRemoveTags([]string{"legacy-profile"}, []string{"Renamed addition"}); err != nil {
			t.Fatal(err)
		}
	}
	ugCompatStoredGroups(t, app, "legacy-profile", a.GroupId, b.GroupId)
	ugCompatMemoryGroups(t, app, "legacy-profile", a.GroupId, b.GroupId)
	ugCompatFindGroup(t, app, "Renamed addition", "") // Empty groups persist.
}

func TestLegacyTagsRejectAmbiguousNamesAtomically(t *testing.T) {
	app := ugCompatApp(t)
	a := ugCompatGroup(t, app, "Branch A", "")
	b := ugCompatGroup(t, app, "Branch B", "")
	ugCompatGroup(t, app, "Shared leaf", a.GroupId)
	ugCompatGroup(t, app, "Shared leaf", b.GroupId)
	ugCompatProfile(t, app, "ambiguous-profile", a.GroupId, b.GroupId)
	before := ugCompatCount(t, app, `SELECT COUNT(*) FROM browser_groups`)
	if err := app.BrowserProfileBatchSetTags([]string{"ambiguous-profile"}, []string{"Must roll back", "Shared leaf"}, false); err == nil {
		t.Fatal("ambiguous legacy name was silently resolved")
	}
	if ugCompatCount(t, app, `SELECT COUNT(*) FROM browser_groups`) != before {
		t.Fatal("failed legacy batch left a newly created group")
	}
	if err := app.BrowserProfileBatchRemoveTags([]string{"ambiguous-profile"}, []string{"Shared leaf"}); err == nil {
		t.Fatal("ambiguous remove should fail")
	}
	if err := app.BrowserRenameTag("Shared leaf", "Unsafe rename"); err == nil {
		t.Fatal("ambiguous rename should fail")
	}
	ugCompatStoredGroups(t, app, "ambiguous-profile", a.GroupId, b.GroupId)
	ugCompatMemoryGroups(t, app, "ambiguous-profile", a.GroupId, b.GroupId)
}
