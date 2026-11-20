package database

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

const upstreamProxy34Description = "实例表持久化代理绑定失效原因"

// SQLite's index_info exposes expression columns as cid=-2, but does not name
// the expression. Check its stored SQL as well as the index flags and columns.
var siblingIndexColumns = regexp.MustCompile(`(?i)\bON\s+browser_groups\s*\(\s*COALESCE\s*\(\s*parent_id\s*,\s*''\s*\)\s*,\s*group_name\s*\)\s*$`)

func (db *DB) migrationState() (version int, upstream bool, err error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var hasHistory, hasObjects bool
	if err := tx.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='schema_migrations'),
		EXISTS(SELECT 1 FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*')`).Scan(&hasHistory, &hasObjects); err != nil {
		return 0, false, fmt.Errorf("读取数据库结构失败: %w", err)
	}
	if !hasHistory {
		if hasObjects {
			return 0, false, fmt.Errorf("现有数据库缺少迁移记录，无法识别来源")
		}
		return 0, false, nil
	}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, false, fmt.Errorf("查询当前 schema 版本失败: %w", err)
	}
	if version < forkGroups34.version {
		return version, false, nil
	}
	upstream, err = requiresUpstream34Bridge(tx)
	return version, upstream, err
}

// A fork and an upstream release both shipped migration 34. Read the recorded
// lineage and its physical schema in a single SQLite snapshot before deciding
// whether the legacy tags and group_id are authoritative.
func requiresUpstream34Bridge(tx *sql.Tx) (bool, error) {
	var version, lastDevelopment, groupObjects, snapshotObjects, proxyColumns int
	var marker34, marker35, marker41, siblingSQL string
	var validLegacy, validProxy, validGroups, validSnapshots bool
	err := tx.QueryRow(`SELECT MAX(version),
		COALESCE(MAX(version) FILTER (WHERE version BETWEEN 36 AND 40), 0),
		COALESCE(MAX(desc) FILTER (WHERE version=34), ''),
		COALESCE(MAX(desc) FILTER (WHERE version=35), ''),
		COALESCE(MAX(desc) FILTER (WHERE version=41), ''),
		(SELECT COUNT(*) FROM pragma_table_info('browser_profiles') WHERE upper(type)='TEXT'
			AND ((name='profile_id' AND pk=1) OR name IN ('group_id','tags')))=3
			AND (SELECT COUNT(*) FROM pragma_table_info('browser_groups') WHERE upper(type)='TEXT'
			AND ((name='group_id' AND pk=1) OR name IN ('group_name','parent_id')))=3,
		(SELECT COUNT(*) FROM pragma_table_info('browser_profiles') WHERE name='proxy_bind_error'),
		EXISTS(SELECT 1 FROM pragma_table_info('browser_profiles') WHERE name='proxy_bind_error'
			AND upper(type)='TEXT' AND "notnull"=1 AND dflt_value=?),
		(SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('browser_profile_groups',
			'idx_browser_profile_groups_group','idx_browser_groups_sibling_name')),
		(SELECT COUNT(*) FROM pragma_table_info('browser_profile_groups') WHERE upper(type)='TEXT' AND "notnull"=1
			AND ((name='profile_id' AND pk=1) OR (name='group_id' AND pk=2)))=2
			AND (SELECT COUNT(*) FROM pragma_foreign_key_list('browser_profile_groups') WHERE on_delete='CASCADE'
				AND (("from"='profile_id' AND "table"='browser_profiles' AND "to"='profile_id') OR
					("from"='group_id' AND "table"='browser_groups' AND "to"='group_id')))=2
			AND EXISTS(SELECT 1 FROM pragma_index_list('browser_profile_groups') WHERE name='idx_browser_profile_groups_group' AND partial=0)
			AND (SELECT group_concat(name,',') FROM (SELECT name FROM pragma_index_info('idx_browser_profile_groups_group') ORDER BY seqno))='group_id,profile_id'
			AND EXISTS(SELECT 1 FROM pragma_index_list('browser_groups') WHERE name='idx_browser_groups_sibling_name' AND "unique"=1 AND partial=0)
			AND EXISTS(SELECT 1 FROM pragma_index_info('idx_browser_groups_sibling_name') WHERE seqno=0 AND cid=-2)
			AND EXISTS(SELECT 1 FROM pragma_index_info('idx_browser_groups_sibling_name') WHERE seqno=1 AND name='group_name'),
		COALESCE((SELECT sql FROM sqlite_schema WHERE type='index' AND name='idx_browser_groups_sibling_name'), ''),
		(SELECT COUNT(*) FROM sqlite_schema WHERE name='browser_chatgpt_accounts'),
		(SELECT COUNT(*) FROM pragma_table_info('browser_chatgpt_accounts') WHERE upper(type)='TEXT'
			AND ((name='profile_id' AND pk=1) OR (name='snapshot_json' AND "notnull"=1)))=2
			AND EXISTS(SELECT 1 FROM pragma_foreign_key_list('browser_chatgpt_accounts')
				WHERE "from"='profile_id' AND "table"='browser_profiles' AND "to"='profile_id' AND on_delete='CASCADE')
		FROM schema_migrations`, "''").Scan(&version, &lastDevelopment, &marker34, &marker35, &marker41, &validLegacy,
		&proxyColumns, &validProxy, &groupObjects, &validGroups, &siblingSQL, &snapshotObjects, &validSnapshots)
	if err != nil {
		return false, fmt.Errorf("读取版本 34 的迁移来源与实际结构失败: %w", err)
	}
	invalid := func(detail string) (bool, error) {
		return false, fmt.Errorf("数据库版本 %d 迁移记录与实际结构不一致（%s）", version, detail)
	}
	if latest := migrations[len(migrations)-1].version; version > latest {
		return invalid(fmt.Sprintf("未知迁移版本 %d，当前程序最高支持 %d", version, latest))
	}
	if !validLegacy || (proxyColumns > 0 && !validProxy) {
		return invalid("实例、分组或 proxy_bind_error 列")
	}
	if marker34 == upstreamProxy34Description && version == 34 {
		if !validProxy || groupObjects != 0 || snapshotObjects != 0 {
			return invalid("上游版本 34 包含缺失或未记录的结构")
		}
		return true, nil
	}
	if marker34 != forkGroups34.desc && !(marker34 == upstreamProxy34Description && version >= mergedSchema41.version) {
		return invalid("无法识别版本 34 的来源")
	}
	if groupObjects != 3 || !validGroups || !siblingIndexColumns.MatchString(siblingSQL) {
		return invalid("分组关系表或索引")
	}
	if version == forkGroups34.version && snapshotObjects != 0 {
		return invalid("快照表缺少版本 35 记录")
	}
	if version >= forkSnapshots35.version && (marker35 != forkSnapshots35.desc || !validSnapshots) {
		return invalid("版本 35 快照表")
	}
	if version >= mergedSchema41.version && (marker41 != mergedSchema41.desc || !validProxy) {
		return invalid("版本 41 整合记录或代理错误列")
	}
	if lastDevelopment != 0 {
		if marker34 != forkGroups34.desc {
			return invalid("上游迁移链包含开发版记录")
		}
		if err := validateDevelopmentHistory(tx, lastDevelopment); err != nil {
			return false, fmt.Errorf("数据库版本 %d 迁移记录与实际结构不一致: %w", version, err)
		}
	}
	return false, nil
}

// These historical versions are read-only provenance checks. Only inspect
// schema metadata; extra business tables, columns, indexes and rows are opaque.
var developmentDescriptions = [...]string{
	"保存实例加密的 GPT 自动登录凭据",
	"保存 GPT 自动续费检查结果与跨实例操作记录",
	"记录 KeyPouch 本机实例关联和只读浏览器会话",
	"复用当前实例的 KeyPouch 登录和聊天标签页",
	"在旧版配置导入后一次性初始化 KeyPouch 实例模板",
}

var developmentIndexFilter = regexp.MustCompile(`(?i)\bWHERE\s+rejected\s*=\s*0\s*$`)

func validateDevelopmentHistory(tx *sql.Tx, last int) error {
	// Development releases applied a contiguous prefix of 36–40. Production
	// reached 41 directly from 35 and therefore has no reserved records.
	for version := 36; version <= last; version++ {
		var matches bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=? AND desc=?)`,
			version, developmentDescriptions[version-36]).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return fmt.Errorf("开发版 %d 的迁移记录缺失或来源未知", version)
		}
	}

	// A recorded version also has to exhibit the structure it introduced.
	// Test only required columns, in particular when an extension added its own.
	var version int
	var table, column string
	err := tx.QueryRow(`WITH required(v, tbl, col, decl, primary_pos, required_value) AS (VALUES
		(36,'browser_chatgpt_credentials','profile_id','TEXT',1,0),
		(36,'browser_chatgpt_credentials','ciphertext','BLOB',0,1),
		(36,'browser_chatgpt_credentials','updated_at','TEXT',0,1),
		(37,'browser_chatgpt_renewal','profile_id','TEXT',1,0),
		(37,'browser_chatgpt_renewal','snapshot_json','TEXT',0,1),
		(37,'browser_chatgpt_renewal_accounts','email','TEXT',1,0),
		(37,'browser_chatgpt_renewal_accounts','state','TEXT',0,1),
		(37,'browser_chatgpt_renewal_accounts','snapshot_json','TEXT',0,1),
		(38,'browser_keypouch_profiles','device_id','TEXT',1,1),
		(38,'browser_keypouch_profiles','record_id','INTEGER',2,1),
		(38,'browser_keypouch_profiles','email','TEXT',0,1),
		(38,'browser_keypouch_profiles','profile_id','TEXT',3,1),
		(38,'browser_keypouch_profiles','rejected','INTEGER',0,1),
		(38,'browser_keypouch_readonly','profile_id','TEXT',1,0),
		(39,'browser_keypouch_readonly','pending_tab','TEXT',0,1),
		(39,'browser_keypouch_readonly','pending_email','TEXT',0,1),
		(39,'browser_keypouch_readonly','chat_tab','TEXT',0,1),
		(39,'browser_keypouch_readonly','chat_email','TEXT',0,1),
		(40,'browser_keypouch_template_init','singleton','INTEGER',1,0)
	) SELECT v,tbl,col FROM required AS r WHERE v <= ? AND (
		NOT EXISTS(SELECT 1 FROM pragma_table_info(r.tbl) AS c WHERE c.name=r.col AND upper(c.type)=r.decl
			AND c.pk=r.primary_pos AND c."notnull"=r.required_value AND (r.v!=39 OR c.dflt_value=?))
		OR (r.col='profile_id' AND NOT EXISTS(SELECT 1 FROM pragma_foreign_key_list(r.tbl) AS fk
			WHERE fk."from"='profile_id' AND fk."table"='browser_profiles' AND fk."to"='profile_id' AND fk.on_delete='CASCADE'))
	) ORDER BY v,tbl,col LIMIT 1`, last, "''").Scan(&version, &table, &column)
	if err == nil {
		return fmt.Errorf("开发版 %d 的 %s.%s 结构不符", version, table, column)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if last < 38 {
		return nil
	}
	for _, index := range []struct{ name, columns string }{
		{"browser_keypouch_record", "device_id,record_id"},
		{"browser_keypouch_profile", "device_id,profile_id"},
	} {
		var uniquePartial bool
		var columns, definition string
		err := tx.QueryRow(`SELECT
			EXISTS(SELECT 1 FROM pragma_index_list('browser_keypouch_profiles') WHERE name=? AND "unique"=1 AND partial=1),
			COALESCE((SELECT group_concat(name,',') FROM (SELECT name FROM pragma_index_info(?) ORDER BY seqno)), ''),
			COALESCE((SELECT sql FROM sqlite_schema WHERE type='index' AND name=?), '')`,
			index.name, index.name, index.name).Scan(&uniquePartial, &columns, &definition)
		if err != nil {
			return err
		}
		if !uniquePartial || columns != index.columns || !developmentIndexFilter.MatchString(definition) {
			return fmt.Errorf("开发版 38 的索引 %s 结构不符", index.name)
		}
	}
	return nil
}

func (db *DB) bridgeUpstream34() error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have finished during the backup. A concurrent writer
	// after this read causes SQLITE_BUSY_SNAPSHOT, so the opener retries safely.
	needed, err := requiresUpstream34Bridge(tx)
	if err != nil || !needed {
		return err
	}
	if err := runMigration(tx, forkGroups34); err != nil {
		return err
	}
	if err := runMigration(tx, forkSnapshots35); err != nil {
		return err
	}
	if err := recordMigration(tx, forkSnapshots35); err != nil {
		return err
	}
	// The upstream 34 marker, applied_at and existing proxy_bind_error values
	// belong to the original database. Its proxy column already matches 41.
	if err := recordMigration(tx, mergedSchema41); err != nil {
		return err
	}
	return tx.Commit()
}
