package backend

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/keypouch"
)

const keyPouchGroupTestSite = "https://groups.example.invalid"

func keyPouchGroupTestApp(t *testing.T) (*App, *keypouch.Device, keyPouchOpenClaim) {
	t.Helper()
	device, err := keypouch.OpenDevice(t.TempDir(), "test", keyPouchGroupTestSite, true)
	if err != nil {
		t.Fatal(err)
	}
	claim := keyPouchOpenClaim{Action: "create-open", RecordID: 18, Email: "groups@example.invalid", GroupID: 3, GroupName: "Work"}
	return ugCompatApp(t), device, claim
}

func keyPouchGroupTestLinks(t *testing.T, device *keypouch.Device) *keypouch.ProfileLinks {
	t.Helper()
	links, err := device.LoadProfileLinks()
	if err != nil {
		t.Fatal(err)
	}
	return links
}

func TestKeyPouchBoundProfileRestoresAndAppendsRequestedGroups(t *testing.T) {
	app, device, claim := keyPouchGroupTestApp(t)
	links := keyPouchGroupTestLinks(t, device)
	parent := ugCompatGroup(t, app, "Local", "")
	ugCompatGroup(t, app, claim.GroupName, parent.GroupId)

	id := keyPouchProfileID(keyPouchGroupTestSite, claim.RecordID)
	resolve := func() {
		t.Helper()
		got, reason := app.keyPouchProfile(keyPouchGroupTestSite, claim, links, func() bool {
			t.Fatal("unexpected association prompt")
			return false
		})
		if got != id || reason != "" {
			t.Fatalf("resolve %q: id=%q reason=%q, want %q", claim.GroupName, got, reason, id)
		}
	}

	// A template copy needs its own top-level destination even when a child has the same name.
	resolve()
	initial := ugCompatFindGroup(t, app, claim.GroupName, "").GroupId
	ugCompatFindGroup(t, app, claim.GroupName, parent.GroupId)
	ugCompatStoredGroups(t, app, id, initial)

	if err := app.BrowserProfileChangeGroups([]string{id}, []string{}, "set"); err != nil {
		t.Fatal(err)
	}
	resolve()
	ugCompatStoredGroups(t, app, id, initial)
	ugCompatMemoryGroups(t, app, id, initial)

	claim.GroupID++
	claim.GroupName = "Moved"
	resolve()
	moved := ugCompatFindGroup(t, app, claim.GroupName, "").GroupId
	stored := ugCompatStoredGroups(t, app, id, initial, moved)
	ugCompatMemoryGroups(t, app, id, initial, moved)
	if stored.GroupId != initial {
		t.Fatalf("primary group after append = %q, want %q", stored.GroupId, initial)
	}
	resolve()
	retried := ugCompatStoredGroups(t, app, id, initial, moved)
	if retried.UpdatedAt != stored.UpdatedAt {
		t.Fatalf("retry changed updated_at from %q to %q", stored.UpdatedAt, retried.UpdatedAt)
	}
}

func TestKeyPouchAdoptionAddsRequestedGroupAfterConfirmation(t *testing.T) {
	app, device, claim := keyPouchGroupTestApp(t)
	links := keyPouchGroupTestLinks(t, device)
	legacy, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: claim.Email, UserDataDir: "existing-data"})
	if err != nil {
		t.Fatal(err)
	}
	beforeAdoption := func() {
		t.Helper()
		if n := ugCompatCount(t, app, `SELECT count(*) FROM browser_groups WHERE group_name=?`, claim.GroupName); n != 0 {
			t.Fatalf("created %d groups named %q before adoption", n, claim.GroupName)
		}
		ugCompatStoredGroups(t, app, legacy.ProfileId)
		ugCompatMemoryGroups(t, app, legacy.ProfileId)
		if links.ProfileID(claim.RecordID) != "" || keyPouchGroupTestLinks(t, device).ProfileID(claim.RecordID) != "" {
			t.Fatal("linked the record before adoption")
		}
	}
	id, reason := app.keyPouchProfile(keyPouchGroupTestSite, claim, links, func() bool {
		beforeAdoption()
		return false
	})
	if id != "" || reason != "cancelled" {
		t.Fatalf("cancelled adoption: id=%q reason=%q", id, reason)
	}
	beforeAdoption()

	local := ugCompatGroup(t, app, "Personal", "")
	id, reason = app.keyPouchProfile(keyPouchGroupTestSite, claim, links, func() bool {
		// The user edited this instance's groups while the confirmation was open.
		if err := app.BrowserProfileChangeGroups([]string{legacy.ProfileId}, []string{local.GroupId}, "add"); err != nil {
			t.Fatal(err)
		}
		return true
	})
	if id != legacy.ProfileId || reason != "" {
		t.Fatalf("confirmed adoption: id=%q reason=%q, want %q", id, reason, legacy.ProfileId)
	}
	requested := ugCompatFindGroup(t, app, claim.GroupName, "")
	stored := ugCompatStoredGroups(t, app, id, local.GroupId, requested.GroupId)
	ugCompatMemoryGroups(t, app, id, local.GroupId, requested.GroupId)
	if stored.UserDataDir != "existing-data" || stored.GroupId != local.GroupId {
		t.Fatalf("adopted profile: directory=%q primary=%q", stored.UserDataDir, stored.GroupId)
	}

	links = keyPouchGroupTestLinks(t, device)
	if got := links.ProfileID(claim.RecordID); got != id {
		t.Fatalf("persisted association = %q, want %q", got, id)
	}
	claim.GroupID++
	claim.GroupName = "Archive"
	archive := ugCompatGroup(t, app, claim.GroupName, "")
	got, reason := app.keyPouchProfile(keyPouchGroupTestSite, claim, links, func() bool {
		t.Fatal("prompted again for an adopted profile")
		return false
	})
	if got != id || reason != "" {
		t.Fatalf("reopen adopted profile: id=%q reason=%q, want %q", got, reason, id)
	}
	ugCompatStoredGroups(t, app, id, local.GroupId, requested.GroupId, archive.GroupId)
	ugCompatMemoryGroups(t, app, id, local.GroupId, requested.GroupId, archive.GroupId)
}

func TestKeyPouchAdoptionRechecksIdentityAfterConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*App, *BrowserProfile) error
	}{
		{"renamed", "profile_conflict", func(app *App, p *BrowserProfile) error {
			_, err := app.BrowserProfileUpdate(p.ProfileId, BrowserProfileInput{
				ProfileName: "renamed@example.invalid", UserDataDir: p.UserDataDir, ProxyId: "__direct__",
			})
			return err
		}},
		{"trashed", "profile_unavailable", func(app *App, p *BrowserProfile) error {
			return app.BrowserProfileDelete(p.ProfileId)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, device, claim := keyPouchGroupTestApp(t)
			legacy, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: claim.Email})
			if err != nil {
				t.Fatal(err)
			}
			links := keyPouchGroupTestLinks(t, device)
			id, reason := app.keyPouchProfile(keyPouchGroupTestSite, claim, links, func() bool {
				if err := tc.change(app, legacy); err != nil {
					t.Fatal(err)
				}
				return true
			})
			if id != "" || reason != tc.reason {
				t.Fatalf("changed candidate: id=%q reason=%q, want %q", id, reason, tc.reason)
			}
			if n := ugCompatCount(t, app, `SELECT count(*) FROM browser_groups WHERE group_name=?`, claim.GroupName); n != 0 {
				t.Fatalf("created %d groups for a changed candidate", n)
			}
			ugCompatStoredGroups(t, app, legacy.ProfileId)
			if got := keyPouchGroupTestLinks(t, device).ProfileID(claim.RecordID); got != "" {
				t.Fatalf("linked changed candidate %q", got)
			}
		})
	}
}

func TestKeyPouchAdoptionGroupWriteFailureDoesNotBind(t *testing.T) {
	app, device, claim := keyPouchGroupTestApp(t)
	local := ugCompatGroup(t, app, "Personal", "")
	legacy, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: claim.Email, GroupIds: []string{local.GroupId}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.GetConn().Exec(`CREATE TRIGGER reject_membership BEFORE INSERT ON browser_profile_groups
		BEGIN SELECT RAISE(ABORT, 'test membership write failure'); END`); err != nil {
		t.Fatal(err)
	}
	links := keyPouchGroupTestLinks(t, device)
	confirmed := false
	id, reason := app.keyPouchProfile(keyPouchGroupTestSite, claim, links, func() bool {
		confirmed = true
		return true
	})
	if !confirmed || id != "" || reason != "group_unavailable" {
		t.Fatalf("failed membership write: confirmed=%t id=%q reason=%q", confirmed, id, reason)
	}
	if n := ugCompatCount(t, app, `SELECT count(*) FROM browser_groups WHERE group_name=?`, claim.GroupName); n != 0 {
		t.Fatalf("failed membership write left %d target groups", n)
	}
	ugCompatStoredGroups(t, app, legacy.ProfileId, local.GroupId)
	ugCompatMemoryGroups(t, app, legacy.ProfileId, local.GroupId)
	if links.ProfileID(claim.RecordID) != "" || keyPouchGroupTestLinks(t, device).ProfileID(claim.RecordID) != "" {
		t.Fatal("linked profile after a failed membership write")
	}
}

func TestKeyPouchTemplateCopyRechecksPersistedIdentity(t *testing.T) {
	for _, collision := range []string{"email", "profile ID"} {
		t.Run(collision, func(t *testing.T) {
			app, device, claim := keyPouchGroupTestApp(t)
			claim.Email = "café@example.invalid"
			template, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: keyPouchTemplate})
			if err != nil {
				t.Fatal(err)
			}
			local, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: "previous@example.invalid", UserDataDir: "existing-local-data"})
			if err != nil {
				t.Fatal(err)
			}
			id := keyPouchProfileID(keyPouchGroupTestSite, claim.RecordID)
			match, reason := app.resolveKeyPouchProfile(context.Background(), keyPouchGroupTestSite, claim, id, "", keyPouchGroupTestLinks(t, device))
			if reason != "" || match.bound != "" || match.candidate != "" || match.template != template.ProfileId {
				t.Fatalf("initial resolution: match=%+v reason=%q", match, reason)
			}

			// Another writer claims the email or the derived ID after resolution.
			// Write through SQLite so the manager still sees its original snapshot.
			competing := *local
			competing.ProfileName = strings.ToUpper(claim.Email)
			if collision == "profile ID" {
				competing.ProfileId, competing.ProfileName, competing.UserDataDir = id, "new-local@example.invalid", "other-local-data"
			}
			if err := app.browserMgr.ProfileDAO.Upsert(&competing); err != nil {
				t.Fatal(err)
			}
			count := ugCompatCount(t, app, `SELECT count(*) FROM browser_profiles`)

			copied, err := app.browserMgr.CopyBoundProfile(match.template, claim.Email, id, claim.GroupName)
			if copied != nil || !errors.Is(err, browser.ErrBoundProfileConflict) {
				t.Fatalf("conflicting copy: profile=%+v err=%v", copied, err)
			}
			if got := ugCompatCount(t, app, `SELECT count(*) FROM browser_profiles`); got != count {
				t.Fatalf("profile count after conflicting copy = %d, want %d", got, count)
			}
			if n := ugCompatCount(t, app, `SELECT count(*) FROM browser_groups WHERE group_name=?`, claim.GroupName); n != 0 {
				t.Fatalf("conflicting copy created %d target groups", n)
			}
			stored := ugCompatStoredGroups(t, app, competing.ProfileId)
			if stored.ProfileName != competing.ProfileName || stored.UserDataDir != competing.UserDataDir {
				t.Fatalf("overwrote competing profile: name=%q directory=%q", stored.ProfileName, stored.UserDataDir)
			}
		})
	}
}
