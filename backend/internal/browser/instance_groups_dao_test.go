package browser

import (
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
)

func instanceGroupDAOFixture(t *testing.T) (*database.DB, *SQLiteProfileDAO, *SQLiteGroupDAO, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "groups.db")
	db, err := database.NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db, NewSQLiteProfileDAO(db.GetConn()), NewSQLiteGroupDAO(db.GetConn()), path
}

func instanceGroupDAOCreate(t *testing.T, dao *SQLiteGroupDAO, name, parent string) *Group {
	t.Helper()
	g, err := dao.Create(GroupInput{GroupName: name, ParentId: parent})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func instanceGroupDAOProfile(t *testing.T, dao *SQLiteProfileDAO, id string, groups []string, deleted bool) *Profile {
	t.Helper()
	p := &Profile{ProfileId: id, ProfileName: id, UserDataDir: id, ProxyId: "__direct__", ProxyConfig: "direct://", GroupIds: CloneGroupIDs(groups), Keywords: []string{"keep-keyword"}}
	if deleted {
		p.DeletedAt = "2026-11-12T00:00:00Z"
	}
	if err := dao.Upsert(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func instanceGroupDAOAssertIDs(t *testing.T, dao *SQLiteProfileDAO, id string, want ...string) *Profile {
	t.Helper()
	p, err := dao.GetById(id)
	if err != nil {
		t.Fatal(err)
	}
	expected := append([]string{}, want...)
	sort.Strings(expected)
	if p.GroupIds == nil || !reflect.DeepEqual(p.GroupIds, expected) {
		t.Fatalf("%s memberships=%v, want %v (nil must remain distinguishable)", id, p.GroupIds, expected)
	}
	return p
}

func instanceGroupDAOAssertStoredProjection(t *testing.T, db *database.DB, dao *SQLiteProfileDAO, id string) {
	t.Helper()
	p, err := dao.GetById(id)
	if err != nil {
		t.Fatal(err)
	}
	var primary, rawTags string
	if err := db.GetConn().QueryRow(`SELECT COALESCE(group_id,''),tags FROM browser_profiles WHERE profile_id=?`, id).Scan(&primary, &rawTags); err != nil {
		t.Fatal(err)
	}
	var tags []string
	if err := json.Unmarshal([]byte(rawTags), &tags); err != nil {
		t.Fatal(err)
	}
	if tags == nil {
		tags = []string{}
	}
	if primary != p.GroupId || !reflect.DeepEqual(tags, p.Tags) {
		t.Fatalf("%s has stale stored compatibility projection: primary=%s tags=%v, canonical primary=%s tags=%v", id, primary, tags, p.GroupId, p.Tags)
	}
}

func TestInstanceGroupsExplicitEmptyClearsWithoutLegacyResurrection(t *testing.T) {
	db, profiles, groups, path := instanceGroupDAOFixture(t)
	a := instanceGroupDAOCreate(t, groups, "A", "")
	b := instanceGroupDAOCreate(t, groups, "B", "")
	p := instanceGroupDAOProfile(t, profiles, "profile", []string{a.GroupId, b.GroupId}, false)
	p.GroupIds = []string{}
	// Deliberately retain a stale compatibility payload. New IDs are authoritative.
	p.GroupId = a.GroupId
	p.Tags = []string{"A", "B"}
	if err := profiles.Upsert(p); err != nil {
		t.Fatal(err)
	}
	instanceGroupDAOAssertIDs(t, profiles, p.ProfileId)
	instanceGroupDAOAssertStoredProjection(t, db, profiles, p.ProfileId)
	if p.GroupIds == nil || len(p.GroupIds) != 0 || p.GroupId != "" || len(p.Tags) != 0 {
		t.Fatalf("caller received a stale projection: %+v", p)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	instanceGroupDAOAssertIDs(t, NewSQLiteProfileDAO(reopened.GetConn()), p.ProfileId)
}

func TestInstanceGroupsLegacyNilPreservesAdditionalMemberships(t *testing.T) {
	_, profiles, groups, _ := instanceGroupDAOFixture(t)
	a := instanceGroupDAOCreate(t, groups, "A", "")
	b := instanceGroupDAOCreate(t, groups, "B", "")
	p := instanceGroupDAOProfile(t, profiles, "profile", []string{a.GroupId, b.GroupId}, false)
	p.GroupIds = nil
	p.GroupId = a.GroupId
	p.Tags = []string{"Legacy addition"}
	if err := profiles.Upsert(p); err != nil {
		t.Fatal(err)
	}
	allGroups, err := groups.List()
	if err != nil {
		t.Fatal(err)
	}
	var addedID string
	for _, g := range allGroups {
		if g.GroupName == "Legacy addition" {
			addedID = g.GroupId
		}
	}
	if addedID == "" {
		t.Fatal("legacy tag write did not create its group")
	}
	instanceGroupDAOAssertIDs(t, profiles, p.ProfileId, a.GroupId, b.GroupId, addedID)
	p.GroupIds = nil
	p.GroupId = ""
	p.Tags = []string{}
	if err := profiles.Upsert(p); err != nil {
		t.Fatal(err)
	}
	instanceGroupDAOAssertIDs(t, profiles, p.ProfileId, a.GroupId, b.GroupId, addedID)
}

func TestInstanceGroupsRejectsBlankIDsInsteadOfClearingOrPartialSuccess(t *testing.T) {
	for _, operation := range []string{"upsert", "batch-groups", "batch-profiles"} {
		t.Run(operation, func(t *testing.T) {
			_, profiles, groups, _ := instanceGroupDAOFixture(t)
			a := instanceGroupDAOCreate(t, groups, "A", "")
			b := instanceGroupDAOCreate(t, groups, "B", "")
			p := instanceGroupDAOProfile(t, profiles, "profile", []string{a.GroupId, b.GroupId}, false)
			var err error
			switch operation {
			case "upsert":
				p.GroupIds = []string{" "}
				err = profiles.Upsert(p)
			case "batch-groups":
				err = profiles.ChangeGroups([]string{"profile"}, []string{""}, "set")
			case "batch-profiles":
				err = profiles.ChangeGroups([]string{"profile", " "}, []string{}, "set")
			}
			if err == nil {
				t.Fatal("invalid nonempty ID list was silently normalized into a destructive or partial operation")
			}
			instanceGroupDAOAssertIDs(t, profiles, "profile", a.GroupId, b.GroupId)
		})
	}
}

func TestInstanceGroupsBatchAddRemoveIsIdempotentAndAtomic(t *testing.T) {
	db, profiles, groups, _ := instanceGroupDAOFixture(t)
	a := instanceGroupDAOCreate(t, groups, "A", "")
	b := instanceGroupDAOCreate(t, groups, "B", "")
	for _, id := range []string{"a-profile", "b-profile"} {
		instanceGroupDAOProfile(t, profiles, id, []string{a.GroupId}, false)
	}
	for repeat := 0; repeat < 2; repeat++ {
		if err := profiles.ChangeGroups([]string{"a-profile", "b-profile", "a-profile"}, []string{b.GroupId, b.GroupId}, "add"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a-profile", "b-profile"} {
		instanceGroupDAOAssertIDs(t, profiles, id, a.GroupId, b.GroupId)
	}
	for repeat := 0; repeat < 2; repeat++ {
		if err := profiles.ChangeGroups([]string{"a-profile", "b-profile"}, []string{a.GroupId}, "remove"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a-profile", "b-profile"} {
		instanceGroupDAOAssertIDs(t, profiles, id, b.GroupId)
	}
	// The valid row sorts first, so this exercises rollback after work was done.
	if err := profiles.ChangeGroups([]string{"a-profile", "z-missing"}, []string{}, "set"); err == nil {
		t.Fatal("batch with a missing instance succeeded")
	}
	instanceGroupDAOAssertIDs(t, profiles, "a-profile", b.GroupId)
	if _, err := db.GetConn().Exec(fmt.Sprintf(`CREATE TRIGGER reject_second_membership
		BEFORE INSERT ON browser_profile_groups
		WHEN NEW.profile_id='b-profile' AND NEW.group_id='%s'
		BEGIN SELECT RAISE(ABORT,'injected second membership failure'); END`, a.GroupId)); err != nil {
		t.Fatal(err)
	}
	if err := profiles.ChangeGroups([]string{"a-profile", "b-profile"}, []string{a.GroupId}, "add"); err == nil {
		t.Fatal("fault-injected batch succeeded")
	}
	for _, id := range []string{"a-profile", "b-profile"} {
		instanceGroupDAOAssertIDs(t, profiles, id, b.GroupId)
		instanceGroupDAOAssertStoredProjection(t, db, profiles, id)
	}
}

func TestInstanceGroupsUpsertFailureLeavesProfileAndRelationshipsUnchanged(t *testing.T) {
	db, profiles, groups, _ := instanceGroupDAOFixture(t)
	a := instanceGroupDAOCreate(t, groups, "A", "")
	b := instanceGroupDAOCreate(t, groups, "B", "")
	original := instanceGroupDAOProfile(t, profiles, "profile", []string{a.GroupId}, false)
	before := *original
	if _, err := db.GetConn().Exec(fmt.Sprintf(`CREATE TRIGGER reject_profile_group_change
		BEFORE INSERT ON browser_profile_groups WHEN NEW.group_id='%s'
		BEGIN SELECT RAISE(ABORT,'injected membership failure'); END`, b.GroupId)); err != nil {
		t.Fatal(err)
	}
	next := *original
	next.ProfileName = "must-not-persist"
	next.GroupIds = []string{b.GroupId}
	inputBefore := next
	if err := profiles.Upsert(&next); err == nil {
		t.Fatal("fault-injected upsert succeeded")
	}
	if !reflect.DeepEqual(next, inputBefore) {
		t.Fatal("failed DAO call mutated the caller's proposed profile")
	}
	after := instanceGroupDAOAssertIDs(t, profiles, "profile", a.GroupId)
	if after.ProfileName != before.ProfileName || !reflect.DeepEqual(after.Keywords, before.Keywords) {
		t.Fatal("failed group write persisted other profile changes")
	}
	instanceGroupDAOAssertStoredProjection(t, db, profiles, "profile")
}

func TestInstanceGroupsDeleteRejectsPromotionConflictAtomically(t *testing.T) {
	db, profiles, groups, _ := instanceGroupDAOFixture(t)
	removed := instanceGroupDAOCreate(t, groups, "Removed", "")
	child := instanceGroupDAOCreate(t, groups, "Collision", removed.GroupId)
	existing := instanceGroupDAOCreate(t, groups, "Collision", "")
	instanceGroupDAOProfile(t, profiles, "profile", []string{removed.GroupId, child.GroupId, existing.GroupId}, false)
	if err := groups.Delete(removed.GroupId); err == nil {
		t.Fatal("deletion with a promotion name collision succeeded")
	}
	actual, err := groups.GetById(child.GroupId)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ParentId != removed.GroupId {
		t.Fatal("failed deletion partially promoted the child")
	}
	if _, err := groups.GetById(removed.GroupId); err != nil {
		t.Fatal("failed deletion removed the parent")
	}
	instanceGroupDAOAssertIDs(t, profiles, "profile", removed.GroupId, child.GroupId, existing.GroupId)
	instanceGroupDAOAssertStoredProjection(t, db, profiles, "profile")
}

func TestInstanceGroupsDeletePromotesSameNameChildAndPreservesOtherMemberships(t *testing.T) {
	db, profiles, groups, path := instanceGroupDAOFixture(t)
	removed := instanceGroupDAOCreate(t, groups, "Same name", "")
	child := instanceGroupDAOCreate(t, groups, "Same name", removed.GroupId)
	other := instanceGroupDAOCreate(t, groups, "Other", "")
	instanceGroupDAOProfile(t, profiles, "active", []string{removed.GroupId, other.GroupId}, false)
	instanceGroupDAOProfile(t, profiles, "trash", []string{removed.GroupId, other.GroupId}, true)
	if err := groups.Delete(removed.GroupId); err != nil {
		t.Fatal(err)
	}
	actual, err := groups.GetById(child.GroupId)
	if err != nil {
		t.Fatal(err)
	}
	if actual.ParentId != "" {
		t.Fatalf("child was not promoted: parent=%s", actual.ParentId)
	}
	for _, id := range []string{"active", "trash"} {
		instanceGroupDAOAssertIDs(t, profiles, id, other.GroupId)
		instanceGroupDAOAssertStoredProjection(t, db, profiles, id)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"active", "trash"} {
		instanceGroupDAOAssertIDs(t, NewSQLiteProfileDAO(reopened.GetConn()), id, other.GroupId)
	}
}

func TestInstanceGroupsRenameRefreshesActiveAndTrashCompatibilityFields(t *testing.T) {
	db, profiles, groups, _ := instanceGroupDAOFixture(t)
	g := instanceGroupDAOCreate(t, groups, "Old name", "")
	instanceGroupDAOProfile(t, profiles, "active", []string{g.GroupId}, false)
	instanceGroupDAOProfile(t, profiles, "trash", []string{g.GroupId}, true)
	if _, err := groups.Update(g.GroupId, GroupInput{GroupName: "New name"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"active", "trash"} {
		p := instanceGroupDAOAssertIDs(t, profiles, id, g.GroupId)
		if !reflect.DeepEqual(p.Tags, []string{"New name"}) {
			t.Fatalf("rename did not update derived names: %v", p.Tags)
		}
		instanceGroupDAOAssertStoredProjection(t, db, profiles, id)
	}
}

func TestInstanceGroupsCountsAndFilteringDeduplicateDescendants(t *testing.T) {
	_, profiles, groups, _ := instanceGroupDAOFixture(t)
	root := instanceGroupDAOCreate(t, groups, "Root", "")
	child := instanceGroupDAOCreate(t, groups, "Child", root.GroupId)
	instanceGroupDAOProfile(t, profiles, "both", []string{root.GroupId, child.GroupId}, false)
	instanceGroupDAOProfile(t, profiles, "child-only", []string{child.GroupId}, false)
	instanceGroupDAOProfile(t, profiles, "trash", []string{root.GroupId, child.GroupId}, true)
	instanceGroupDAOProfile(t, profiles, "ungrouped", []string{}, false)
	counts, err := profiles.CountByGroup()
	if err != nil {
		t.Fatal(err)
	}
	if counts[root.GroupId] != 2 || counts[child.GroupId] != 2 {
		t.Fatalf("counts double-counted a profile or included trash: %v", counts)
	}
	listed, err := profiles.ListByGroup(root.GroupId, true, []string{child.GroupId})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, p := range listed {
		if ids[p.ProfileId] {
			t.Fatal("duplicate profile in descendant listing")
		}
		ids[p.ProfileId] = true
	}
	if len(listed) != 2 || !ids["both"] || !ids["child-only"] {
		t.Fatalf("wrong descendant selection: %v", ids)
	}
	ungrouped, err := profiles.ListByGroup("", true, []string{child.GroupId})
	if err != nil {
		t.Fatal(err)
	}
	if len(ungrouped) != 1 || ungrouped[0].ProfileId != "ungrouped" {
		t.Fatalf("ungrouped query did not require zero memberships: %+v", ungrouped)
	}
}

func TestInstanceGroupsHierarchyAndConcurrentSiblingCreationRejectInvalidWrites(t *testing.T) {
	_, _, groups, _ := instanceGroupDAOFixture(t)
	root := instanceGroupDAOCreate(t, groups, "Root", "")
	child := instanceGroupDAOCreate(t, groups, "Child", root.GroupId)
	if _, err := groups.Update(root.GroupId, GroupInput{GroupName: "Root", ParentId: child.GroupId}); err == nil {
		t.Fatal("hierarchy cycle was accepted")
	}
	if _, err := groups.Create(GroupInput{GroupName: "Invalid", ParentId: "missing"}); err == nil {
		t.Fatal("missing parent was accepted")
	}
	if _, err := groups.Create(GroupInput{GroupName: "Root"}); err == nil {
		t.Fatal("duplicate sibling name was accepted")
	}
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := groups.Create(GroupInput{GroupName: "Concurrent"})
			results <- err
		}()
	}
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent duplicate creation successes=%d, want exactly 1", succeeded)
	}
	all, err := groups.List()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, g := range all {
		if g.GroupName == "Concurrent" && g.ParentId == "" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent requests persisted %d duplicate roots", count)
	}
}

func TestInstanceGroupsCopyAndRecyclePreserveAllMemberships(t *testing.T) {
	_, profiles, groups, path := instanceGroupDAOFixture(t)
	a := instanceGroupDAOCreate(t, groups, "A", "")
	b := instanceGroupDAOCreate(t, groups, "B", "")
	instanceGroupDAOProfile(t, profiles, "source", []string{a.GroupId, b.GroupId}, false)
	m := NewManager(&config.Config{}, filepath.Dir(path))
	m.ProfileDAO = profiles
	m.GroupDAO = groups
	copy, err := m.CopyWithMode("source", "copied", "regular")
	if err != nil {
		t.Fatal(err)
	}
	instanceGroupDAOAssertIDs(t, profiles, copy.ProfileId, a.GroupId, b.GroupId)
	if err := m.Delete(copy.ProfileId); err != nil {
		t.Fatal(err)
	}
	if p := instanceGroupDAOAssertIDs(t, profiles, copy.ProfileId, a.GroupId, b.GroupId); p.DeletedAt == "" {
		t.Fatal("copy was not moved to trash")
	}
	if _, err := m.Restore(copy.ProfileId); err != nil {
		t.Fatal(err)
	}
	if p := instanceGroupDAOAssertIDs(t, profiles, copy.ProfileId, a.GroupId, b.GroupId); p.DeletedAt != "" {
		t.Fatal("copy was not restored")
	}
	if err := m.Delete(copy.ProfileId); err != nil {
		t.Fatal(err)
	}
	if err := m.PermanentlyDelete(copy.ProfileId); err != nil {
		t.Fatal(err)
	}
	var dangling int
	if err := profiles.db.QueryRow(`SELECT COUNT(*) FROM browser_profile_groups WHERE profile_id=?`, copy.ProfileId).Scan(&dangling); err != nil {
		t.Fatal(err)
	}
	if dangling != 0 {
		t.Fatalf("physical deletion left %d dangling memberships", dangling)
	}
	instanceGroupDAOAssertIDs(t, profiles, "source", a.GroupId, b.GroupId)
}
