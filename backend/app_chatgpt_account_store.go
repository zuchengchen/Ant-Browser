package backend

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BrowserChatGPTAccountGet only reads the local snapshot. It never starts a
// browser or contacts ChatGPT; nil means this instance has no saved result.
func (a *App) BrowserChatGPTAccountGet(profileID string) (*ChatGPTAccountInfo, error) {
	profileID = strings.TrimSpace(profileID)
	if a == nil || a.db == nil || profileID == "" {
		return nil, fmt.Errorf("账户信息数据库尚未就绪或未选择有效实例")
	}
	var data string
	err := a.db.GetConn().QueryRow(`SELECT c.snapshot_json FROM browser_chatgpt_accounts c
		JOIN browser_profiles p ON p.profile_id=c.profile_id
		WHERE c.profile_id=? AND COALESCE(p.deleted_at, '')=''`, profileID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取已保存的 GPT 账户信息失败：%w", err)
	}
	var info ChatGPTAccountInfo
	if json.Unmarshal([]byte(data), &info) != nil || info.ProfileID != profileID || info.SavedAt == "" {
		return nil, fmt.Errorf("已保存的 GPT 账户信息无效，请重新获取")
	}
	return &info, nil
}

func (a *App) saveChatGPTAccount(info *ChatGPTAccountInfo) error {
	info.SavedAt = ""
	if info.Status != "ok" && info.Status != "partial" {
		return nil
	}
	if strings.TrimSpace(info.Email) == "" {
		return fmt.Errorf("未读取到登录邮箱，已保留之前保存的信息")
	}
	if a == nil || a.db == nil {
		return fmt.Errorf("账户信息数据库尚未就绪")
	}
	snapshot := *info
	snapshot.SavedAt = time.Now().Format(time.RFC3339Nano)
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	// Checking the active profile in this statement also handles a profile being
	// deleted while the network read was in progress. Each new read replaces the
	// whole snapshot, so unavailable fields cannot inherit stale account values.
	result, err := a.db.GetConn().Exec(`INSERT INTO browser_chatgpt_accounts(profile_id, snapshot_json)
		SELECT profile_id, ? FROM browser_profiles WHERE profile_id=? AND COALESCE(deleted_at, '')=''
		ON CONFLICT(profile_id) DO UPDATE SET snapshot_json=excluded.snapshot_json`, string(data), info.ProfileID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("实例已删除，未保存账户信息")
	}
	info.SavedAt = snapshot.SavedAt
	return nil
}
