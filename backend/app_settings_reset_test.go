package backend

import (
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/logger"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResetManagedSettingsResetsManagedConfigAndSecrets(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	cfg := config.DefaultConfig()
	cfg.App.Name = "custom app"
	cfg.Logging.Level = "warn"
	logger.Init(context.Background(), cfg.Logging.Level)
	t.Cleanup(func() { _ = logger.Close() })
	cfg.Browser.UserDataRoot = filepath.Join(root, "browser-data")
	cfg.Automation.Enabled = true
	cfg.Automation.NodeSource = config.AutomationNodeSourceSystem
	cfg.LaunchServer.Port = 23456
	cfg.LaunchServer.Auth.Enabled = true
	cfg.LaunchServer.Auth.APIKey = "launch-key"
	cfg.Backup.Channels.OpenList.BaseURL = "https://openlist.example.com/dav"
	cfg.Backup.Channels.OpenList.Token = "openlist-token"
	cfg.Backup.Channels.S3.Endpoint = "https://s3.example.com"
	cfg.Backup.Channels.S3.Bucket = "backup-bucket"
	cfg.Backup.Channels.S3.AccessKeyID = "access-key"
	cfg.Backup.Channels.S3.SecretAccessKey = "secret-key"
	cfg.Backup.Schedule.Enabled = true
	cfg.Backup.Schedule.DailyTime = "03:15"
	app.config = cfg

	if err := cfg.Save(filepath.Join(root, "config.yaml")); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := saveBackupLocalConfig(filepath.Join(root, backupLocalConfigFileName), cfg.Backup); err != nil {
		t.Fatalf("save local backup config: %v", err)
	}

	scheduler := newBackupScheduler(app)
	scheduler.settings = cfg.Backup
	scheduler.configurationError = "old configuration error"
	scheduler.state = backupScheduleState{
		Status:         backupScheduleStatusSuccess,
		LastRunAt:      "2026-09-02T01:00:00Z",
		LastSuccessAt:  "2026-09-02T01:01:00Z",
		LastRemoteName: "backup.zip",
	}
	scheduler.lastDate = "2026-09-02"
	app.backupScheduler = scheduler

	if err := app.ResetManagedSettings(); err != nil {
		t.Fatalf("reset managed settings: %v", err)
	}

	defaults := config.DefaultConfig()
	if app.config.Automation != defaults.Automation {
		t.Fatalf("automation config = %+v, want %+v", app.config.Automation, defaults.Automation)
	}
	if !backupConfigsEqual(app.config.Backup, defaults.Backup) {
		t.Fatalf("backup config = %+v, want %+v", app.config.Backup, defaults.Backup)
	}
	if app.config.LaunchServer != defaults.LaunchServer {
		t.Fatalf("LaunchServer config = %+v, want %+v", app.config.LaunchServer, defaults.LaunchServer)
	}
	if app.config.Logging.Level != defaults.Logging.Level || app.GetLogLevel() != "INFO" {
		t.Fatalf("log level was not reset: config=%s, runtime=%s", app.config.Logging.Level, app.GetLogLevel())
	}
	if app.config.App.Name != "custom app" {
		t.Fatalf("unmanaged app settings changed: %q", app.config.App.Name)
	}
	if app.config.Browser.UserDataRoot != filepath.Join(root, "browser-data") {
		t.Fatalf("unmanaged browser settings changed: %q", app.config.Browser.UserDataRoot)
	}

	if _, err := os.Stat(filepath.Join(root, backupLocalConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("backup local config still exists, stat error: %v", err)
	}

	if !backupConfigsEqual(scheduler.settings, defaults.Backup) {
		t.Fatalf("scheduler settings = %+v, want %+v", scheduler.settings, defaults.Backup)
	}
	if scheduler.configurationError != "" || scheduler.state.Status != backupScheduleStatusNever || scheduler.lastDate != "" || !scheduler.nextAttemptAt.IsZero() || scheduler.running || scheduler.resetting {
		t.Fatalf("scheduler state was not reset: error=%q state=%+v lastDate=%q nextAttemptAt=%v running=%v resetting=%v", scheduler.configurationError, scheduler.state, scheduler.lastDate, scheduler.nextAttemptAt, scheduler.running, scheduler.resetting)
	}

	persisted, err := config.Load(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if persisted.Automation != defaults.Automation || !backupConfigsEqual(persisted.Backup, defaults.Backup) || persisted.LaunchServer != defaults.LaunchServer || persisted.Logging.Level != defaults.Logging.Level {
		t.Fatalf("persisted managed settings were not reset: automation=%+v backup=%+v launchServer=%+v", persisted.Automation, persisted.Backup, persisted.LaunchServer)
	}
	if persisted.App.Name != "custom app" || persisted.Browser.UserDataRoot != filepath.Join(root, "browser-data") {
		t.Fatalf("persisted unmanaged settings changed: app=%+v browser.userDataRoot=%q", persisted.App, persisted.Browser.UserDataRoot)
	}
}

func TestSaveLogLevelPersistsAndUpdatesExistingLoggers(t *testing.T) {
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()
	logger.Init(context.Background(), app.config.Logging.Level)
	t.Cleanup(func() { _ = logger.Close() })
	active := logger.New("active component")
	path := app.resolveAppPath("config.yaml")
	if err := app.SaveLogLevel(" Debug "); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Logging.Level != "debug" || app.GetLogLevel() != "DEBUG" || !active.ShouldLog(logger.DEBUG) {
		t.Fatalf("saved=%s, runtime=%s, existing logger=%s", saved.Logging.Level, app.GetLogLevel(), active.GetLevel())
	}
	if err := app.SaveLogLevel("verbose"); err == nil {
		t.Fatal("invalid log level was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveLogLevel("error"); err == nil {
		t.Fatal("expected save to fail with a directory as its target")
	}
	if app.config.Logging.Level != "debug" || active.GetLevel() != logger.DEBUG {
		t.Fatal("failed save changed the active log level")
	}
}

func TestResetManagedSettingsRequiresInitializedConfig(t *testing.T) {
	app := NewApp(t.TempDir())
	if err := app.ResetManagedSettings(); err == nil {
		t.Fatal("reset should fail when config is not initialized")
	}
}
