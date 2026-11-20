package database

import "testing"

func TestInstanceGroupsV34RejectsMissingCanonicalStructure(t *testing.T) {
	for _, scenario := range []string{"missing-membership-table", "missing-unique-index"} {
		t.Run(scenario, func(t *testing.T) {
			db, _ := instanceGroupsLegacyDB(t)
			instanceGroupsLegacyProfile(t, db, "profile", "", []string{"Retained"}, false)
			if scenario == "missing-membership-table" {
				instanceGroupsLegacyExec(t, db, `INSERT INTO schema_migrations(version,desc) VALUES(34,'foreign branch marker')`)
			} else {
				if err := db.Migrate(); err != nil {
					t.Fatal(err)
				}
				instanceGroupsLegacyExec(t, db, `DROP INDEX idx_browser_groups_sibling_name`)
			}
			if err := db.Migrate(); err == nil {
				t.Fatal("inconsistent v34 structure was silently accepted")
			}
		})
	}
}
