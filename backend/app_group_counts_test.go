//go:build linux

package backend

import (
	"ant-chrome/backend/internal/browser"
	"errors"
	"testing"
)

func TestListGroupsCountsPersistedActiveProfiles(t *testing.T) {
	app, db := linuxPerfGroupFixture(t, 100)
	// Preload stale manager data, then move one row directly, as group actions
	// currently do. Counts must come from persisted data rather than that cache.
	app.browserMgr.InitData()
	if _, err := db.GetConn().Exec(`UPDATE browser_profiles SET group_id='perf-group-20' WHERE profile_id='perf-profile-00000'`); err != nil {
		t.Fatal(err)
	}
	groups := app.ListGroups()
	if len(groups) != 21 {
		t.Fatalf("group count=%d expected=21", len(groups))
	}
	for index, group := range groups {
		want := 5
		if index == 0 {
			want = 4
		} else if index == 20 {
			want = 1
		}
		if group.InstanceCount != want {
			t.Fatalf("group %s count=%d expected=%d", group.GroupId, group.InstanceCount, want)
		}
	}
}

func TestListGroupsSupportsDAOWithoutAggregateCapability(t *testing.T) {
	app, _ := linuxPerfGroupFixture(t, 100)
	fallback := &groupCountFallbackDAO{
		ProfileDAO: app.browserMgr.ProfileDAO,
		profiles: []*browser.Profile{
			{GroupId: "perf-group-02"}, {GroupId: "perf-group-02"}, {GroupId: ""},
		},
	}
	app.browserMgr.ProfileDAO = fallback
	groups := app.ListGroups()
	if fallback.calls != 1 || len(groups) != 21 || groups[2].InstanceCount != 2 || groups[0].InstanceCount != 0 {
		t.Fatalf("fallback behavior: calls=%d groups=%+v", fallback.calls, groups)
	}
}

func TestListGroupsDoesNotPresentFailedCountsAsZero(t *testing.T) {
	for _, mode := range []string{"aggregate", "list", "missing"} {
		t.Run(mode, func(t *testing.T) {
			app, _ := linuxPerfGroupFixture(t, 100)
			fallback := &groupCountFallbackDAO{ProfileDAO: app.browserMgr.ProfileDAO, err: errors.New("test read failure")}
			switch mode {
			case "aggregate":
				app.browserMgr.ProfileDAO = &groupCountFailureDAO{groupCountFallbackDAO: fallback}
			case "list":
				app.browserMgr.ProfileDAO = fallback
			case "missing":
				app.browserMgr.ProfileDAO = nil
			}
			if groups := app.ListGroups(); len(groups) != 0 {
				t.Fatalf("failure presented as real group counts: %+v", groups)
			}
			if mode == "aggregate" && fallback.calls != 0 {
				t.Fatal("aggregate failure fell back to another data source")
			}
		})
	}
}

type groupCountFallbackDAO struct {
	browser.ProfileDAO
	profiles []*browser.Profile
	err      error
	calls    int
}

func (d *groupCountFallbackDAO) List() ([]*browser.Profile, error) {
	d.calls++
	return d.profiles, d.err
}

type groupCountFailureDAO struct {
	*groupCountFallbackDAO
}

func (d *groupCountFailureDAO) CountByGroup() (map[string]int, error) {
	return nil, errors.New("test aggregate failure")
}
