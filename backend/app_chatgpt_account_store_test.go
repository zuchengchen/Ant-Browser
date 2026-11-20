package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/database"
	"path/filepath"
	"reflect"
	"testing"
)

func chatGPTStoreFixture(t *testing.T) (*App, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.db")
	db, err := database.NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.Migrate(); err != nil {
		t.Fatal(err)
	}
	dao := browser.NewSQLiteProfileDAO(db.GetConn())
	for _, id := range []string{"first", "second"} {
		if err := dao.Upsert(&browser.Profile{ProfileId: id, ProfileName: id}); err != nil {
			t.Fatal(err)
		}
	}
	// No browser manager: saved information must be readable without launching.
	return &App{db: db}, path
}

func chatGPTStoreSample() ChatGPTAccountInfo {
	zero, cards := float64(0), 2
	return ChatGPTAccountInfo{
		ProfileID: "first", Status: "ok", CheckedAt: "2026-11-12T14:00:00+08:00",
		DisplayName: "Example", Username: "@example", Email: "sample@example.test", Plan: "Plus",
		RenewalDate: "Dec 7, 2026", Weekly: ChatGPTUsageWindow{RemainingPercent: &zero, ResetText: "Resets in 2d"},
		Credits: &zero, ResetCards: &cards, Warnings: []string{},
	}
}

func TestChatGPTSnapshotSurvivesReopenAndRemainsInstanceSpecific(t *testing.T) {
	app, path := chatGPTStoreFixture(t)
	info := chatGPTStoreSample()
	if err := app.saveChatGPTAccount(&info); err != nil || info.SavedAt == "" {
		t.Fatalf("save: %v, savedAt=%q", err, info.SavedAt)
	}
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := database.NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	reopened := &App{db: db}
	got, err := reopened.BrowserChatGPTAccountGet(" first ")
	if err != nil || !reflect.DeepEqual(got, &info) {
		t.Fatalf("reopen lost snapshot/null/zero: %+v, %v", got, err)
	}
	if got, err := reopened.BrowserChatGPTAccountGet("second"); err != nil || got != nil {
		t.Fatalf("another instance inherited account: %+v, %v", got, err)
	}
	// Editing/renaming the original instance must not erase its saved account.
	if err := browser.NewSQLiteProfileDAO(db.GetConn()).Upsert(&browser.Profile{ProfileId: "first", ProfileName: "renamed"}); err != nil {
		t.Fatal(err)
	}
	got, err = reopened.BrowserChatGPTAccountGet("first")
	if err != nil || !reflect.DeepEqual(got, &info) {
		t.Fatal("profile edit lost account snapshot", err)
	}
}

func TestChatGPTSnapshotFailedReadsPreservePreviousResult(t *testing.T) {
	app, _ := chatGPTStoreFixture(t)
	info := chatGPTStoreSample()
	if err := app.saveChatGPTAccount(&info); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"login_required", "verification_required", "account_mismatch", "error", "partial"} {
		t.Run(status, func(t *testing.T) {
			attempt := ChatGPTAccountInfo{ProfileID: "first", Status: status}
			err := app.saveChatGPTAccount(&attempt)
			if status == "partial" && err == nil {
				t.Fatal("accepted unidentified account")
			}
			if attempt.SavedAt != "" {
				t.Fatal("failed attempt marked as saved")
			}
			got, err := app.BrowserChatGPTAccountGet("first")
			if err != nil || !reflect.DeepEqual(got, &info) {
				t.Fatal("failed attempt changed saved snapshot", err)
			}
		})
	}
}

func TestChatGPTSnapshotPartialRefreshReplacesWholeSnapshot(t *testing.T) {
	app, _ := chatGPTStoreFixture(t)
	info := chatGPTStoreSample()
	if err := app.saveChatGPTAccount(&info); err != nil {
		t.Fatal(err)
	}
	info.Status, info.Plan, info.Credits, info.ResetCards = "partial", "Free", nil, nil
	info.RenewalDate = ""
	info.CheckedAt = "2026-11-13T14:00:00+08:00"
	info.Warnings = []string{"Usage page unavailable"}
	if err := app.saveChatGPTAccount(&info); err != nil {
		t.Fatal(err)
	}
	got, err := app.BrowserChatGPTAccountGet("first")
	if err != nil || !reflect.DeepEqual(got, &info) {
		t.Fatalf("partial read reused old fields: %+v, %v", got, err)
	}
}

func TestChatGPTSnapshotProfileDeletionAndRestore(t *testing.T) {
	app, _ := chatGPTStoreFixture(t)
	info := chatGPTStoreSample()
	if err := app.saveChatGPTAccount(&info); err != nil {
		t.Fatal(err)
	}
	dao := browser.NewSQLiteProfileDAO(app.db.GetConn())
	if err := dao.SoftDelete("first", "2026-11-12T15:00:00+08:00"); err != nil {
		t.Fatal(err)
	}
	attempt := info
	if err := app.saveChatGPTAccount(&attempt); err == nil || attempt.SavedAt != "" {
		t.Fatal("saved a result after instance moved to trash")
	}
	if got, err := app.BrowserChatGPTAccountGet("first"); err != nil || got != nil {
		t.Fatal("returned trashed instance snapshot", err)
	}
	if err := dao.Restore("first"); err != nil {
		t.Fatal(err)
	}
	got, err := app.BrowserChatGPTAccountGet("first")
	if err != nil || !reflect.DeepEqual(got, &info) {
		t.Fatal("restore lost original snapshot", err)
	}
	if err := dao.Delete("first"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := app.db.GetConn().QueryRow(`SELECT COUNT(*) FROM browser_chatgpt_accounts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("permanent deletion left account information: count=%d, err=%v", count, err)
	}
	if err := app.saveChatGPTAccount(&attempt); err == nil {
		t.Fatal("saved orphaned account information")
	}
}

func TestChatGPTSnapshotStorageFailuresAreNotReportedAsSaved(t *testing.T) {
	app, _ := chatGPTStoreFixture(t)
	info := chatGPTStoreSample()
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.saveChatGPTAccount(&info); err == nil || info.SavedAt != "" {
		t.Fatal("database failure reported as successful save")
	}
	if _, err := app.BrowserChatGPTAccountGet("first"); err == nil {
		t.Fatal("database failure returned empty success")
	}
}
