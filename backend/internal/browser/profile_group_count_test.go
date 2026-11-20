package browser

import (
	"ant-chrome/backend/internal/database"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSQLiteProfileDAOCountByGroupExcludesDeletedAndUngrouped(t *testing.T) {
	db, err := database.NewDB(filepath.Join(t.TempDir(), "group-counts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B", "a"} {
		if _, err := db.GetConn().Exec(`INSERT INTO browser_groups(group_id,group_name) VALUES (?,?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	dao := NewSQLiteProfileDAO(db.GetConn())
	for _, id := range []string{"target", "untouched"} {
		if err := dao.Upsert(&Profile{ProfileId: id, ProfileName: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.GetConn().Exec(`INSERT INTO browser_profile_groups(profile_id,group_id) VALUES ('target','A')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetConn().Exec(`INSERT INTO browser_profile_groups(profile_id,group_id) VALUES ('untouched','B'); UPDATE browser_profiles SET deleted_at='2026-01-02T00:00:00Z' WHERE profile_id='untouched'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"no-group", "null-group", "case-distinct"} {
		profile := &Profile{ProfileId: id, ProfileName: id}
		if id == "case-distinct" {
			profile.GroupId = "a"
		}
		if err := dao.Upsert(profile); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.GetConn().Exec(`UPDATE browser_profiles SET group_id=NULL WHERE profile_id='null-group'`); err != nil {
		t.Fatal(err)
	}
	counts, err := dao.CountByGroup()
	if err != nil || !reflect.DeepEqual(counts, map[string]int{"A": 1, "a": 1}) {
		t.Fatalf("counts=%v error=%v", counts, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if counts, err := dao.CountByGroup(); err == nil || counts != nil {
		t.Fatalf("closed database should fail: counts=%v error=%v", counts, err)
	}
}
