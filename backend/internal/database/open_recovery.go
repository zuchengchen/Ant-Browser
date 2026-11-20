package database

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// RecoveryInfo 一次启动期数据库自愈的结果。
type RecoveryInfo struct {
	Recovered        bool   // 是否重建了空库（业务数据未恢复）
	Reason           string // 触发自愈的原始错误
	MigrationBackup  string // 升级前的数据快照（正常升级路径）
	BrokenBackupPath string // 损坏库的文件级备份
	BrokenParkedPath string // 损坏库的现场保留路径（同目录 .broken-*）
}

const (
	dbRecoveryMigrateAttempts = 3
	dbRecoveryRetryInterval   = 300 * time.Millisecond
)

// OpenWithRecovery 在存量库升级前备份。只有打开时明确报告物理损坏才备份原文件并重建。
// 返回的 RecoveryInfo.Recovered=true 表示使用了兜底重建，调用方应显式告知用户。
func OpenWithRecovery(dbPath string, backupDir string) (*DB, RecoveryInfo, error) {
	info := RecoveryInfo{}
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return nil, info, fmt.Errorf("数据库路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, info, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	// 打开前先判定是否存量库：NewDB 会创建库文件，之后再判断会把新装误判为升级。
	hadContent := databaseFileHasContent(dbPath)
	db, err := NewDB(dbPath)
	if err != nil {
		info.Reason = err.Error()
		var sqliteErr *sqlite.Error
		if hadContent && errors.As(err, &sqliteErr) {
			switch sqliteErr.Code() & 0xff {
			case 11, 26: // SQLITE_CORRUPT, SQLITE_NOTADB
				return recoverDatabase(dbPath, backupDir, info, err)
			}
		}
		return nil, info, openDatabaseError(err)
	}

	if hadContent {
		version, _, stateErr := db.migrationState()
		if stateErr != nil {
			_ = db.Close()
			info.Reason = stateErr.Error()
			return nil, info, openDatabaseError(stateErr)
		}
		if version >= migrations[len(migrations)-1].version {
			return db, info, nil
		}
		info.MigrationBackup, err = db.BackupTo(backupDir, "app-pre-migrate")
		if err != nil {
			_ = db.Close()
			info.Reason = err.Error()
			return nil, info, openDatabaseError(fmt.Errorf("迁移前备份失败: %w", err))
		}
	}

	if err := migrateWithRetry(db, dbRecoveryMigrateAttempts, info.MigrationBackup != ""); err != nil {
		info.Reason = err.Error()
		_ = db.Close()
		return nil, info, openDatabaseError(err)
	}
	return db, info, nil
}

func openDatabaseError(err error) error {
	if isDatabaseBusyError(err) {
		return fmt.Errorf("数据库正被其他进程占用，请关闭另一个应用实例后重试: %w", err)
	}
	return err
}

// recoverDatabase only receives a SQLite physical-corruption error. Build the
// replacement before changing the original pathname or any of its sidecars.
func recoverDatabase(dbPath string, backupDir string, info RecoveryInfo, cause error) (*DB, RecoveryInfo, error) {
	lock := dbPath + ".recovery-lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		return nil, info, fmt.Errorf("无法独占损坏库以恢复（%s）: %w", lock, err)
	}
	defer os.Remove(lock)

	// A different opener may have finished recovery between our open and lock.
	existing, err := NewDB(dbPath)
	if err == nil {
		_ = existing.Close()
		return OpenWithRecovery(dbPath, backupDir)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || (sqliteErr.Code()&0xff != 11 && sqliteErr.Code()&0xff != 26) {
		return nil, info, openDatabaseError(err)
	}
	info.BrokenBackupPath, err = backupDatabaseFiles(dbPath, backupDir)
	if err != nil {
		return nil, info, fmt.Errorf("损坏库备份失败，原文件已保留: %w（原始错误: %v）", err, cause)
	}

	stage, err := os.MkdirTemp(filepath.Dir(dbPath), ".db-recovery-")
	if err != nil {
		return nil, info, err
	}
	defer os.RemoveAll(stage)
	replacement := filepath.Join(stage, filepath.Base(dbPath))
	db, err := NewDB(replacement)
	if err != nil {
		return nil, info, fmt.Errorf("新建替代数据库失败: %w", err)
	}
	migrateErr := migrateWithRetry(db, 2, false)
	if err := errors.Join(migrateErr, db.Close()); err != nil {
		return nil, info, fmt.Errorf("准备替代数据库失败，原文件已保留: %w", err)
	}

	parked := dbPath + ".broken-" + filepath.Base(filepath.Dir(info.BrokenBackupPath))
	if err := renameDatabaseFiles(dbPath, parked); err != nil {
		return nil, info, fmt.Errorf("保留损坏库现场失败: %w", err)
	}
	info.BrokenParkedPath = parked
	if err := renameDatabaseFiles(replacement, dbPath); err != nil {
		restoreErr := renameDatabaseFiles(parked, dbPath)
		return nil, info, fmt.Errorf("安装替代库失败（原库备份 %s）: %w", info.BrokenBackupPath, errors.Join(err, restoreErr))
	}
	db, err = NewDB(dbPath)
	if err != nil {
		return nil, info, fmt.Errorf("打开替代库失败（原库现场 %s）: %w", parked, err)
	}
	info.Recovered = true
	return db, info, nil
}

// databaseFileHasContent 判断库文件是否已存在且有内容（区分全新安装与存量升级）。
func databaseFileHasContent(dbPath string) bool {
	info, err := os.Stat(dbPath)
	return err == nil && info.Size() > 0
}

// migrateWithRetry 对 database is locked / busy 做有限重试，其余错误立即返回。
func migrateWithRetry(db *DB, attempts int, hasSnapshot bool) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = db.migrate(hasSnapshot)
		if err == nil {
			return nil
		}
		if !isDatabaseBusyError(err) || attempt == attempts-1 {
			return err
		}
		time.Sleep(dbRecoveryRetryInterval)
	}
	return err
}

func isDatabaseBusyError(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		// SQLITE_BUSY(5) / SQLITE_LOCKED(6)：另一个连接或进程正在写。
		code := sqliteErr.Code() & 0xff
		return code == 5 || code == 6
	}
	// 兜底：错误被跨层转述时按文本判断（避免路径/文案误判依赖错误码优先）。
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "locked") || strings.Contains(message, "busy")
}

// A corrupt database cannot reliably VACUUM. Keep its files together in a
// unique private directory; fail before moving anything if any copy fails.
func backupDatabaseFiles(dbPath string, backupDir string) (string, error) {
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(backupDir, "app-db-broken-"+time.Now().Format("20060102-150405")+"-")
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(dbPath))
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		err := copyDatabaseFile(dbPath+suffix, dst+suffix)
		if suffix != "" && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dst, nil
}

func copyDatabaseFile(src string, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	return errors.Join(copyErr, target.Sync(), target.Close())
}

// Move sidecars first and the main database last. Undo partial moves so a
// failed rename cannot leave the original separated from committed WAL pages.
func renameDatabaseFiles(from string, to string) error {
	var moved []string
	for _, suffix := range []string{"-journal", "-shm", "-wal", ""} {
		err := os.Rename(from+suffix, to+suffix)
		if suffix != "" && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				err = errors.Join(err, os.Rename(to+moved[i], from+moved[i]))
			}
			return err
		}
		moved = append(moved, suffix)
	}
	return nil
}
