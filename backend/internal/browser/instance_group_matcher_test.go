package browser

import (
	"ant-chrome/backend/internal/config"
	"errors"
	"reflect"
	"testing"
)

func TestInstanceGroupMatcherFindsSecondMembershipAndAncestorsWithAND(t *testing.T) {
	_, _, groups, _ := instanceGroupDAOFixture(t)
	parent := instanceGroupDAOCreate(t, groups, "Parent", "")
	child := instanceGroupDAOCreate(t, groups, "Child", parent.GroupId)
	extra := instanceGroupDAOCreate(t, groups, "Extra", "")
	m := NewManager(&config.Config{}, t.TempDir())
	m.GroupDAO = groups
	both := Profile{GroupId: extra.GroupId, GroupIds: []string{extra.GroupId, child.GroupId}}
	childOnly := Profile{GroupId: child.GroupId, GroupIds: []string{child.GroupId}}
	extraOnly := Profile{GroupId: extra.GroupId, GroupIds: []string{extra.GroupId}}
	for _, selector := range []struct {
		id           string
		names        []string
		childMatches bool
	}{
		{parent.GroupId, nil, true},
		{child.GroupId, nil, true},
		{"", []string{"Parent", "Extra"}, false},
		{parent.GroupId, []string{"Extra"}, false},
	} {
		matcher, err := m.InstanceGroupMatcher(selector.id, selector.names)
		if err != nil {
			t.Fatal(err)
		}
		if !matcher(both) || matcher(childOnly) != selector.childMatches || matcher(extraOnly) {
			t.Fatalf("incorrect AND/descendant match for %+v", selector)
		}
	}
	matcher, err := m.InstanceGroupMatcher(parent.GroupId, nil)
	if err != nil {
		t.Fatal(err)
	}
	if matcher(Profile{GroupId: child.GroupId, GroupIds: []string{}}) {
		t.Fatal("explicitly empty canonical IDs fell back to stale groupId")
	}
	if !matcher(Profile{GroupId: child.GroupId, GroupIds: nil}) {
		t.Fatal("legacy single group fallback no longer works")
	}
}

func TestInstanceGroupMatcherRejectsAmbiguousNamesButAcceptsIDs(t *testing.T) {
	_, _, groups, _ := instanceGroupDAOFixture(t)
	parent := instanceGroupDAOCreate(t, groups, "Parent", "")
	root := instanceGroupDAOCreate(t, groups, "Shared", "")
	child := instanceGroupDAOCreate(t, groups, "Shared", parent.GroupId)
	m := NewManager(&config.Config{}, t.TempDir())
	m.GroupDAO = groups
	if _, err := m.InstanceGroupMatcher("", []string{" shared "}); err == nil {
		t.Fatal("ambiguous cross-branch legacy name was accepted")
	}
	matcher, err := m.InstanceGroupMatcher(child.GroupId, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !matcher(Profile{GroupIds: []string{child.GroupId}}) || matcher(Profile{GroupIds: []string{root.GroupId}}) {
		t.Fatal("ID selection did not disambiguate same-name groups")
	}
	if _, err := m.InstanceGroupMatcher("missing", nil); err == nil {
		t.Fatal("missing group ID was accepted")
	}
}

type instanceGroupListFailure struct{ GroupDAO }

func (instanceGroupListFailure) List() ([]*Group, error) {
	return nil, errors.New("injected catalog read failure")
}

func TestInstanceGroupMatcherPropagatesCatalogFailure(t *testing.T) {
	m := NewManager(&config.Config{}, t.TempDir())
	m.GroupDAO = instanceGroupListFailure{}
	if _, err := m.InstanceGroupMatcher("group", nil); err == nil {
		t.Fatal("catalog failure became an empty match")
	}
}

func TestInstanceGroupSnapshotReadFailureRollsBackMutation(t *testing.T) {
	for _, operation := range []string{"membership", "rename", "delete"} {
		t.Run(operation, func(t *testing.T) {
			db, profiles, groups, _ := instanceGroupDAOFixture(t)
			a := instanceGroupDAOCreate(t, groups, "A", "")
			b := instanceGroupDAOCreate(t, groups, "B", "")
			instanceGroupDAOProfile(t, profiles, "target", []string{a.GroupId}, false)
			instanceGroupDAOProfile(t, profiles, "unreadable", []string{}, false)
			// SQLite accepts this TEXT value in the INTEGER-affinity column. The
			// canonical profile scanner then fails when reading the batch snapshot.
			if _, err := db.GetConn().Exec(`UPDATE browser_profiles SET memory_limit_mb='not-an-integer' WHERE profile_id='unreadable'`); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "membership":
				_, err = profiles.ChangeGroupsWithSnapshot([]string{"target"}, []string{b.GroupId}, "set")
			case "rename":
				_, _, err = profiles.ChangeGroupDefinition(a.GroupId, &GroupInput{GroupName: "Renamed"})
			case "delete":
				_, _, err = profiles.ChangeGroupDefinition(a.GroupId, nil)
			}
			if err == nil {
				t.Fatal("mutation committed despite its unreadable pre-commit snapshot")
			}
			instanceGroupDAOAssertIDs(t, profiles, "target", a.GroupId)
			instanceGroupDAOAssertStoredProjection(t, db, profiles, "target")
			actual, err := groups.GetById(a.GroupId)
			if err != nil {
				t.Fatal(err)
			}
			if actual.GroupName != "A" {
				t.Fatal("group definition changed despite snapshot failure")
			}
		})
	}
}

func TestInstanceGroupLegacyImportCreatesRootAndPreservesSingleGroup(t *testing.T) {
	db, profiles, groups, _ := instanceGroupDAOFixture(t)
	parent := instanceGroupDAOCreate(t, groups, "Parent", "")
	child := instanceGroupDAOCreate(t, groups, "Legacy tag", parent.GroupId)
	instanceGroupDAOProfile(t, profiles, "profile", []string{child.GroupId}, false)
	var previous []string
	for repeat := 0; repeat < 2; repeat++ {
		tx, err := db.GetConn().Begin()
		if err != nil {
			t.Fatal(err)
		}
		legacy := &Profile{ProfileId: "profile", GroupId: child.GroupId, Tags: []string{"Legacy tag"}, GroupIds: nil}
		if err := PersistLegacyImportedProfileGroups(tx, legacy); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		all, err := groups.List()
		if err != nil {
			t.Fatal(err)
		}
		var rootID string
		for _, g := range all {
			if g.GroupName == "Legacy tag" && g.ParentId == "" {
				rootID = g.GroupId
			}
		}
		if rootID == "" || rootID == child.GroupId {
			t.Fatal("old label was redirected to a same-name child")
		}
		p := instanceGroupDAOAssertIDs(t, profiles, "profile", rootID, child.GroupId)
		if repeat > 0 && !reflect.DeepEqual(previous, p.GroupIds) {
			t.Fatal("repeating legacy import created new relationships")
		}
		previous = CloneGroupIDs(p.GroupIds)
	}
}

func TestInstanceGroupLegacyListAdapterUsesDescendantsAndRejectsAmbiguity(t *testing.T) {
	_, profiles, groups, _ := instanceGroupDAOFixture(t)
	parent := instanceGroupDAOCreate(t, groups, "Parent", "")
	child := instanceGroupDAOCreate(t, groups, "Child", parent.GroupId)
	instanceGroupDAOProfile(t, profiles, "profile", []string{child.GroupId}, false)
	m := NewManager(&config.Config{}, t.TempDir())
	m.ProfileDAO = profiles
	m.GroupDAO = groups
	matched, err := m.ListByGroupName(" Parent ")
	if err != nil || len(matched) != 1 || matched[0].ProfileId != "profile" {
		t.Fatalf("legacy name adapter did not find descendants: profiles=%v error=%v", matched, err)
	}
	instanceGroupDAOCreate(t, groups, "Child", "")
	if _, err := m.ListByGroupName("Child"); err == nil {
		t.Fatal("legacy name adapter silently combined ambiguous branches")
	}
}

func TestInstanceGroupLegacyFileListAdapterPreservesWhitespaceNormalization(t *testing.T) {
	m := NewManager(&config.Config{}, t.TempDir())
	m.Profiles["profile"] = &Profile{ProfileId: "profile", Tags: []string{"Tag"}}
	matched, err := m.ListByGroupName(" Tag ")
	if err != nil || len(matched) != 1 || matched[0].ProfileId != "profile" {
		t.Fatalf("file-backed old tag selector lost trimming behavior: profiles=%v error=%v", matched, err)
	}
}
