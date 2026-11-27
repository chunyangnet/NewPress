package database

import "testing"

func TestPostgreSQLMembershipSeedTiersPreserveExistingSites(t *testing.T) {
	for _, scenario := range []string{"configured", "account_exists", "custom_plan"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, pool := openSetupIntegrationPool(t)
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
				UPDATE np_progress_settings SET data=data #- '{membership,plans,0,tier}' #- '{membership,plans,1,tier}';
				DELETE FROM newpress_schema_migrations WHERE version='0051_membership_seed_tiers.sql';
			`); err != nil {
				t.Fatal(err)
			}
			var mutation string
			switch scenario {
			case "configured":
				mutation = `UPDATE np_progress_settings SET data=jsonb_set(data,'{version}','"2"')`
			case "account_exists":
				mutation = `INSERT INTO np_users(username,email,password_hash) VALUES('existing-member','member@example.test','test');
					INSERT INTO np_progress_accounts(user_id,experience,membership) SELECT id,0,'{"version":"0"}' FROM np_users WHERE username='existing-member'`
			case "custom_plan":
				mutation = `UPDATE np_progress_settings SET data=jsonb_set(data,'{membership,plans,1,price}','"250"')`
			}
			if _, err := pool.Exec(ctx, mutation); err != nil {
				t.Fatal(err)
			}
			var before, after string
			if err := pool.QueryRow(ctx, `SELECT data::text FROM np_progress_settings WHERE id=1`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := Migrate(ctx, pool); err != nil {
					t.Fatal(err)
				}
			}
			if err := pool.QueryRow(ctx, `SELECT data::text FROM np_progress_settings WHERE id=1`).Scan(&after); err != nil || after != before {
				t.Fatalf("existing membership settings changed: %v", err)
			}
		})
	}
}
