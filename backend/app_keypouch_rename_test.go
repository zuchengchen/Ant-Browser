package backend

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/keypouch"
)

func keyPouchRenameFixture(t *testing.T, adopted bool) (*App, *keypouch.Device, keyPouchOpenClaim, string) {
	t.Helper()
	isolateKeyPouchUser(t)
	app, device, claim := keyPouchGroupTestApp(t)
	want := keyPouchProfileID(keyPouchGroupTestSite, claim.RecordID)
	if adopted {
		p, err := app.BrowserProfileCreate(BrowserProfileInput{
			ProfileName: claim.Email, UserDataDir: "adopted-data", Keywords: []string{"keep"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want = p.ProfileId
	}
	prompted := false
	id, reason := app.keyPouchProfile(keyPouchGroupTestSite, claim, keyPouchGroupTestLinks(t, device), func() bool {
		prompted = true
		return true
	})
	if id != want || reason != "" || prompted != adopted {
		t.Fatalf("initial binding: id=%q reason=%q prompted=%t", id, reason, prompted)
	}
	return app, device, claim, id
}

func TestKeyPouchRemoteRenameReusesBoundProfile(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		adopted, cached, old bool
		localAlias           string
	}{
		{name: "stable ID", cached: true, old: true},
		{name: "confirmed legacy ID", adopted: true, cached: true, old: true},
		{name: "local alias and remote account both changed", cached: true, old: true, localAlias: "local caption"},
		{name: "old open-only profile with partial cache", adopted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, device, claim, id := keyPouchRenameFixture(t, tc.adopted)
			manager := app.browserMgr
			p := manager.Profiles[id]
			before := copyBrowserProfileSnapshot(p)
			if tc.old {
				if err := app.saveLocalGPTCredential(id, localGPTCredential{Site: keyPouchGroupTestSite, RecordID: claim.RecordID, Email: claim.Email}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.localAlias != "" {
				if _, err := app.db.GetConn().Exec(`UPDATE browser_profiles SET profile_name=? WHERE profile_id=?`, tc.localAlias, id); err != nil {
					t.Fatal(err)
				}
				p.ProfileName = tc.localAlias
			}
			if tc.cached {
				// No browser runs in this fixture. A retained live descriptor
				// verifies the resolver updates only its name and memberships.
				p.Running, p.DebugReady, p.Pid, p.DebugPort = true, true, 271, 272
			} else {
				delete(manager.Profiles, id)
			}
			beforeCount := ugCompatCount(t, app, `SELECT count(*) FROM browser_profiles`)
			claim.Email, claim.GroupName = "After+Case@example.invalid", "Moved group"
			open := func() {
				t.Helper()
				got, reason := app.keyPouchOpenProfile(context.Background(), keyPouchGroupTestSite, claim, device)
				if reason != "" || got != id {
					t.Fatalf("reopen after remote rename: id=%q reason=%q", got, reason)
				}
			}
			open()
			group := ugCompatFindGroup(t, app, claim.GroupName, "")
			saved := ugCompatStoredGroups(t, app, id, before.GroupId, group.GroupId)
			if saved.ProfileName != claim.Email || saved.UserDataDir != before.UserDataDir || saved.GroupId != before.GroupId ||
				saved.ProxyId != before.ProxyId || !slices.Equal(saved.Keywords, before.Keywords) || !slices.Equal(saved.FingerprintArgs, before.FingerprintArgs) {
				t.Fatal("rename did not preserve the profile's directory, settings and memberships")
			}
			current := manager.Profiles[id]
			if current == nil || current.ProfileName != claim.Email || current.UserDataDir != before.UserDataDir ||
				(tc.cached && (current != p || !current.Running || !current.DebugReady || current.Pid != 271 || current.DebugPort != 272)) {
				t.Fatal("rename replaced or lost the running or newly loaded profile")
			}
			if old, err := app.readLocalGPTCredential(id); err != nil || old != nil {
				t.Fatal("previous account still available after committed rename")
			}
			open()
			retried := ugCompatStoredGroups(t, app, id, before.GroupId, group.GroupId)
			if retried.UpdatedAt != saved.UpdatedAt || ugCompatCount(t, app, `SELECT count(*) FROM browser_profiles`) != beforeCount {
				t.Fatal("retry rewrote the profile or created a duplicate")
			}
		})
	}
}

func TestKeyPouchRemoteRenamePreservesRealConflicts(t *testing.T) {
	for _, kind := range []string{"local edit only", "new name belongs to another record", "bound to another site", "bound to another record"} {
		t.Run(kind, func(t *testing.T) {
			app, device, claim, id := keyPouchRenameFixture(t, false)
			old := localGPTCredential{Site: keyPouchGroupTestSite, RecordID: claim.RecordID, Email: claim.Email}
			localName := claim.Email
			claim.Email = "café-new@example.invalid"
			switch kind {
			case "local edit only":
				localName, claim.Email = "user-edited label", old.Email
				if _, err := app.db.GetConn().Exec(`UPDATE browser_profiles SET profile_name=? WHERE profile_id=?`, localName, id); err != nil {
					t.Fatal(err)
				}
				app.browserMgr.Profiles[id].ProfileName = localName
			case "new name belongs to another record":
				competing := claim
				competing.RecordID++
				competing.Email = strings.ToUpper(competing.Email)
				if other, reason := app.keyPouchOpenProfile(context.Background(), keyPouchGroupTestSite, competing, device); reason != "" || other == id {
					t.Fatalf("could not create the independent binding: %q", reason)
				}
			case "bound to another site":
				old.Site = "https://different.example.invalid"
			case "bound to another record":
				old.RecordID++
			}
			if err := app.saveLocalGPTCredential(id, old); err != nil {
				t.Fatal(err)
			}
			claim.GroupName = "Unapplied"
			got, reason := app.keyPouchOpenProfile(context.Background(), keyPouchGroupTestSite, claim, device)
			if got != "" || reason != "profile_conflict" {
				t.Fatalf("conflicting binding: id=%q reason=%q", got, reason)
			}
			saved, err := app.browserMgr.ProfileDAO.GetById(id)
			if err != nil || saved.ProfileName != localName || app.browserMgr.Profiles[id].ProfileName != localName ||
				ugCompatCount(t, app, `SELECT count(*) FROM browser_groups WHERE group_name=?`, claim.GroupName) != 0 {
				t.Fatal("conflict changed the profile")
			}
			if previous, err := app.readLocalGPTCredential(id); err != nil || previous == nil || *previous != old {
				t.Fatal("conflict changed the saved credential")
			}
		})
	}
}

func TestKeyPouchDoesNotAdoptAnotherSitesSavedAccount(t *testing.T) {
	isolateKeyPouchUser(t)
	app, device, claim := keyPouchGroupTestApp(t)
	p, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: claim.Email})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.saveLocalGPTCredential(p.ProfileId, localGPTCredential{
		Site: "https://another.example.invalid", RecordID: claim.RecordID, Email: claim.Email,
	}); err != nil {
		t.Fatal(err)
	}
	id, reason := app.keyPouchProfile(keyPouchGroupTestSite, claim, keyPouchGroupTestLinks(t, device), func() bool {
		t.Fatal("offered a profile belonging to another site")
		return false
	})
	if id != "" || reason != "profile_conflict" || keyPouchGroupTestLinks(t, device).ProfileID(claim.RecordID) != "" {
		t.Fatal("lost the previous profile's ownership")
	}
}

func TestKeyPouchRenameRejectsTwoBoundProfileIDs(t *testing.T) {
	app, device, claim, adopted := keyPouchRenameFixture(t, true)
	derived := keyPouchProfileID(keyPouchGroupTestSite, claim.RecordID)
	// A restored package can introduce the deterministic profile while the
	// local device still points at a different, explicitly associated ID.
	ugCompatProfile(t, app, derived)
	original := claim.Email
	claim.Email = "next@example.invalid"
	got, reason := app.keyPouchOpenProfile(context.Background(), keyPouchGroupTestSite, claim, device)
	if got != "" || reason != "profile_conflict" {
		t.Fatalf("duplicate bound IDs: id=%q reason=%q", got, reason)
	}
	if app.browserMgr.Profiles[adopted].ProfileName != original || app.browserMgr.Profiles[derived].ProfileName == claim.Email {
		t.Fatal("an ambiguous binding changed a name")
	}
}

func TestKeyPouchBoundRenameRechecksIdentityInWriteTransaction(t *testing.T) {
	for _, kind := range []string{"name taken", "profile renamed", "profile trashed"} {
		t.Run(kind, func(t *testing.T) {
			app, _, claim, id := keyPouchRenameFixture(t, false)
			called := false
			edit := &browser.BoundProfileRename{From: claim.Email, To: "café@example.invalid", BeforeCommit: func() error {
				called = true
				return nil
			}}
			var err error
			switch kind {
			case "name taken":
				_, err = app.BrowserProfileCreate(BrowserProfileInput{ProfileName: strings.ToUpper(edit.To)})
			case "profile renamed":
				_, err = app.db.GetConn().Exec(`UPDATE browser_profiles SET profile_name='later' WHERE profile_id=?`, id)
			case "profile trashed":
				err = app.browserMgr.ProfileDAO.SoftDelete(id, "2026-11-22T01:00:00Z")
			}
			if err != nil {
				t.Fatal(err)
			}
			dao := app.browserMgr.ProfileDAO.(*browser.SQLiteProfileDAO)
			_, _, err = dao.UpdateBoundProfile(id, "unused", edit)
			if !errors.Is(err, browser.ErrBoundProfileConflict) || called ||
				ugCompatCount(t, app, `SELECT count(*) FROM browser_groups WHERE group_name='unused'`) != 0 {
				t.Fatal("write transaction accepted a stale profile or changed its account")
			}
		})
	}
}

func TestKeyPouchRenameRollsBackNameGroupAndCredential(t *testing.T) {
	for _, stage := range []string{"group write", "commit"} {
		t.Run(stage, func(t *testing.T) {
			app, device, claim, id := keyPouchRenameFixture(t, false)
			before := copyBrowserProfileSnapshot(app.browserMgr.Profiles[id])
			old := localGPTCredential{Site: keyPouchGroupTestSite, RecordID: claim.RecordID, Email: claim.Email, Password: "synthetic", TOTPSecret: "synthetic"}
			if err := app.saveLocalGPTCredential(id, old); err != nil {
				t.Fatal(err)
			}
			vault, err := keypouch.LocalGPTVaultDirectory()
			if err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(vault, "*", "*.cred"))
			if err != nil || len(files) != 1 {
				t.Fatal("expected one private synthetic credential")
			}
			ciphertextHash := func() [32]byte {
				t.Helper()
				b, err := os.ReadFile(files[0])
				if err != nil {
					t.Fatal(err)
				}
				return sha256.Sum256(b)
			}
			original := ciphertextHash()
			db := app.db.GetConn()
			trigger := `CREATE TRIGGER reject_update BEFORE INSERT ON browser_profile_groups BEGIN SELECT RAISE(ABORT, 'group write failure'); END`
			wantReason := "group_unavailable"
			if stage == "commit" {
				if _, err := db.Exec(`CREATE TABLE test_deferred (id TEXT REFERENCES browser_groups(group_id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
					t.Fatal(err)
				}
				trigger = `CREATE TRIGGER reject_update AFTER UPDATE OF profile_name ON browser_profiles BEGIN INSERT INTO test_deferred VALUES ('missing'); END`
				wantReason = "storage_error"
			}
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			claim.Email, claim.GroupName = "next@example.invalid", "New group"
			if got, reason := app.keyPouchOpenProfile(context.Background(), keyPouchGroupTestSite, claim, device); got != "" || reason != wantReason {
				t.Fatalf("write failure: id=%q reason=%q", got, reason)
			}
			saved := ugCompatStoredGroups(t, app, id, before.GroupId)
			if saved.ProfileName != before.ProfileName || saved.UpdatedAt != before.UpdatedAt ||
				app.browserMgr.Profiles[id].ProfileName != before.ProfileName ||
				ugCompatCount(t, app, `SELECT count(*) FROM browser_groups WHERE group_name=?`, claim.GroupName) != 0 {
				t.Fatal("failed commit changed the name or group")
			}
			if previous, err := app.readLocalGPTCredential(id); err != nil || previous == nil || *previous != old {
				t.Fatal("failed rename did not preserve the previous credential")
			}
			if stage == "group write" && ciphertextHash() != original {
				t.Fatal("failed group write touched the previous encrypted file")
			}
			if _, err := db.Exec(`DROP TRIGGER reject_update`); err != nil {
				t.Fatal(err)
			}
			if got, reason := app.keyPouchOpenProfile(context.Background(), keyPouchGroupTestSite, claim, device); reason != "" || got != id {
				t.Fatalf("valid retry after failure: id=%q reason=%q", got, reason)
			}
		})
	}
}
