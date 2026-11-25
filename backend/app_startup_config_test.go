package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"ant-chrome/backend/internal/logger"
)

func TestStartupAndReloadPreserveInvalidConfig(t *testing.T) {
	for name, document := range map[string]string{
		"malformed": "database:\n  sqlite:\n    path: data/custom.db\nbrowser:\n  default_connector_type: mihomo\nbroken: [\n",
		"empty":     "# interrupted write\n",
		"multiple":  "database: {}\n---\nbrowser: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			app := NewApp(t.TempDir())
			path := app.resolveAppPath("config.yaml")
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := app.startup(context.Background()); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("startup error should identify the invalid configuration: %v", err)
			}
			t.Cleanup(app.stopRuntimeEvents)
			if app.IsAppReady() || app.db != nil || app.browserMgr != nil {
				t.Fatal("startup initialized database or browser services with defaults")
			}
			if _, err := os.Stat(filepath.Join(app.appRoot, "data", "app.db")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("default database was touched: %v", err)
			}
			loaded := config.DefaultConfig()
			loaded.Browser.DefaultConnectorType = config.BrowserConnectorMihomo
			app.config = loaded
			if err := app.ReloadConfig(); err == nil || app.config != loaded {
				t.Fatalf("failed reload replaced the active config: %v", err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != document {
				t.Fatalf("invalid config changed: %q, %v", got, err)
			}
		})
	}
}

func TestDatabaseRecoveryNoticeRemainsAvailableAfterStartupEvent(t *testing.T) {
	app := NewApp(t.TempDir())
	log := logger.New("App")
	app.reportDatabaseRecovery(log, database.RecoveryInfo{MigrationBackup: "pre-upgrade.db"})
	if notice := app.GetDatabaseRecoveryNotice(); notice != nil {
		t.Fatalf("ordinary migration showed a recovery notice: %v", notice)
	}
	app.reportDatabaseRecovery(log, database.RecoveryInfo{
		Recovered: true, Reason: "invalid database", BrokenBackupPath: "backup.db", BrokenParkedPath: "old.db",
	})
	notice := app.GetDatabaseRecoveryNotice()
	if notice["reason"] != "invalid database" || notice["backupPath"] != "backup.db" || notice["parkedPath"] != "old.db" {
		t.Fatalf("recovery notice = %v", notice)
	}
	notice["backupPath"] = "changed"
	if app.GetDatabaseRecoveryNotice()["backupPath"] != "backup.db" {
		t.Fatal("caller was able to overwrite the recovery notice")
	}
}
