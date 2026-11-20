package backend

import (
	"ant-chrome/backend/internal/automation"
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func automationInstanceGroupFixture(t *testing.T) (*App, map[string]string) {
	t.Helper()
	root := t.TempDir()
	db, err := database.NewDB(filepath.Join(root, "automation-groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	groupDAO := browser.NewSQLiteGroupDAO(db.GetConn())
	profileDAO := browser.NewSQLiteProfileDAO(db.GetConn())
	ids := map[string]string{}
	for _, name := range []string{"Parent", "Child", "Extra", "Forbidden"} {
		parent := ""
		if name == "Child" {
			parent = ids["Parent"]
		}
		g, err := groupDAO.Create(browser.GroupInput{GroupName: name, ParentId: parent})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = g.GroupId
	}
	for _, fixture := range []struct {
		id     string
		groups []string
	}{
		{"target", []string{ids["Extra"], ids["Child"]}},
		{"child-only", []string{ids["Child"]}},
		{"other", []string{ids["Forbidden"]}},
	} {
		if err := profileDAO.Upsert(&browser.Profile{ProfileId: fixture.id, ProfileName: fixture.id, UserDataDir: fixture.id, GroupId: fixture.groups[0], GroupIds: fixture.groups, Keywords: []string{"fixture-keyword"}, ProxyId: "__direct__", ProxyConfig: "direct://"}); err != nil {
			t.Fatal(err)
		}
	}
	m := browser.NewManager(&config.Config{}, root)
	m.ProfileDAO = profileDAO
	m.GroupDAO = groupDAO
	m.InitData()
	m.Profiles["target"].LaunchCode = "111111"
	m.Profiles["other"].LaunchCode = "222222"
	return &App{browserMgr: m}, ids
}

func TestAutomationInstanceGroupSelectorsUseAncestorsAndAND(t *testing.T) {
	app, ids := automationInstanceGroupFixture(t)
	for _, fixture := range []struct {
		selector automation.ScriptTargetSelector
		want     []string
	}{
		{automation.ScriptTargetSelector{GroupID: ids["Child"]}, []string{"child-only", "target"}},
		{automation.ScriptTargetSelector{GroupID: ids["Parent"]}, []string{"child-only", "target"}},
		{automation.ScriptTargetSelector{Tags: []string{"Parent", "Extra"}}, []string{"target"}},
		{automation.ScriptTargetSelector{GroupID: ids["Parent"], Tags: []string{"Extra"}}, []string{"target"}},
	} {
		profiles, err := app.resolveAutomationTargetProfiles(fixture.selector, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, p := range profiles {
			got = append(got, p.ProfileId)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, fixture.want) {
			t.Fatalf("selector=%+v profiles=%v want %v", fixture.selector, got, fixture.want)
		}
	}
}

func TestAutomationExactTargetPreservesAllSelectorConditions(t *testing.T) {
	app, ids := automationInstanceGroupFixture(t)
	for _, fixture := range []struct {
		name     string
		selector automation.ScriptTargetSelector
	}{
		{"profile-and-group", automation.ScriptTargetSelector{ProfileID: "target", GroupID: ids["Forbidden"]}},
		{"code-and-group", automation.ScriptTargetSelector{Code: "111111", GroupID: ids["Forbidden"]}},
		{"profile-and-name", automation.ScriptTargetSelector{ProfileID: "target", ProfileName: "other"}},
		{"profile-and-keyword", automation.ScriptTargetSelector{ProfileID: "target", Keywords: []string{"definitely-unmatched"}}},
		{"profile-and-code", automation.ScriptTargetSelector{ProfileID: "target", Code: "222222"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if p, err := app.resolveAutomationExactTargetProfile(fixture.selector, "fixture"); err == nil {
				t.Fatalf("AND mismatch was ignored; selected profile=%s", p.ProfileId)
			}
		})
	}
	matched, err := app.resolveAutomationExactTargetProfile(automation.ScriptTargetSelector{ProfileID: "target", Code: "111111", GroupID: ids["Parent"], Tags: []string{"Extra"}}, "fixture")
	if err != nil || matched.ProfileId != "target" {
		t.Fatalf("valid AND selector failed: profile=%s error=%v", matched.ProfileId, err)
	}
}

func TestAutomationExistingAndCreateModesRejectNonmatchingGroup(t *testing.T) {
	for _, mode := range []string{"existing", "create"} {
		t.Run(mode, func(t *testing.T) {
			app, ids := automationInstanceGroupFixture(t)
			selector := automation.ScriptTargetSelector{ProfileID: "target", GroupID: ids["Forbidden"]}
			script := automation.ScriptRecord{ID: "fixture", Name: "fixture", TargetConfig: automation.ScriptTargetConfig{Mode: mode, Selector: selector, TemplateSelector: selector}}
			before := len(app.browserMgr.List())
			if _, _, err := app.resolveAutomationScriptTarget(script); err == nil {
				t.Fatal("script ignored the nonmatching group condition")
			}
			if after := len(app.browserMgr.List()); after != before {
				t.Fatalf("failed template selector created an instance: before=%d after=%d", before, after)
			}
		})
	}
}

func TestAutomationExactSelectorEnrichmentDoesNotRewriteConflictingCode(t *testing.T) {
	app, _ := automationInstanceGroupFixture(t)
	input := automation.ScriptTargetSelector{ProfileID: "target", Code: "222222"}
	actual := app.enrichAutomationExactTargetSelector(input)
	if actual.ProfileID != input.ProfileID || actual.Code != input.Code {
		t.Fatalf("enrichment silently changed user conditions: before=%+v after=%+v", input, actual)
	}
}

func TestAutomationInstanceGroupSelectorRejectsAmbiguousName(t *testing.T) {
	app, ids := automationInstanceGroupFixture(t)
	for _, parent := range []string{"", ids["Parent"]} {
		if _, err := app.browserMgr.GroupDAO.Create(browser.GroupInput{GroupName: "Shared", ParentId: parent}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.resolveAutomationTargetProfiles(automation.ScriptTargetSelector{Tags: []string{"Shared"}}, "fixture"); err == nil {
		t.Fatal("ambiguous group name was accepted")
	}
	if _, err := app.resolveAutomationExactTargetProfile(automation.ScriptTargetSelector{ProfileID: "target", Tags: []string{"Shared"}}, "fixture"); err == nil {
		t.Fatal("profile ID bypassed an ambiguous group condition")
	}
}
