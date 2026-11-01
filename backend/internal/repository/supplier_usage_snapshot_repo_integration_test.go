//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

var snapshotDay = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

type snapshotFixture struct {
	t *testing.T
	c *sql.Conn
	r *supplierUsageSnapshotRepository
}

func snapshotSetup(t *testing.T, partition bool) *snapshotFixture {
	t.Helper()
	c, err := integrationDB.Conn(context.Background())
	require.NoError(t, err)
	name := fmt.Sprintf("snapshot_%d", time.Now().UnixNano())
	_, err = c.ExecContext(context.Background(), "CREATE SCHEMA "+name+"; SET search_path TO "+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.ExecContext(context.Background(), "SET TIME ZONE 'UTC'; SET search_path TO public; DROP SCHEMA "+name+" CASCADE")
		_ = c.Close()
	})
	f := &snapshotFixture{t: t, c: c, r: &supplierUsageSnapshotRepository{db: c}}
	f.exec(`CREATE TABLE accounts (id bigint PRIMARY KEY,supplier_user_id bigint,name text,status text DEFAULT 'active',schedulable boolean DEFAULT true,supplier_paused boolean DEFAULT false,parent_account_id bigint,deleted_at timestamptz,temp_unschedulable_until timestamptz); CREATE TABLE account_groups(account_id bigint);`)
	usage := `CREATE TABLE usage_logs(account_id bigint,model text,created_at timestamptz,input_tokens bigint DEFAULT 1,output_tokens bigint DEFAULT 2,cache_creation_tokens bigint DEFAULT 3,cache_read_tokens bigint DEFAULT 4,input_cost numeric DEFAULT .01,output_cost numeric DEFAULT .02,cache_creation_cost numeric DEFAULT .03,cache_read_cost numeric DEFAULT .04,total_cost numeric)`
	if partition {
		usage += ` PARTITION BY RANGE(created_at); CREATE TABLE usage_logs_oct PARTITION OF usage_logs FOR VALUES FROM ('2026-10-01') TO ('2026-11-01')`
	}
	f.exec(usage)
	migration, err := migrations.FS.ReadFile("249_supplier_usage_daily.sql")
	require.NoError(t, err)
	f.exec(string(migration))
	f.exec(`INSERT INTO accounts(id,supplier_user_id,name) VALUES(1,11,'one'),(2,22,'two'),(3,NULL,'platform'); INSERT INTO account_groups VALUES(1),(2),(3)`)
	return f
}
func (f *snapshotFixture) exec(q string, args ...any) {
	f.t.Helper()
	_, e := f.c.ExecContext(context.Background(), q, args...)
	require.NoError(f.t, e)
}
func (f *snapshotFixture) log(id int, model, cost string) {
	f.exec(`INSERT INTO usage_logs(account_id,model,total_cost,created_at) VALUES($1,$2,$3,$4)`, id, model, cost, snapshotDay.Add(time.Hour))
}
func (f *snapshotFixture) refresh(sample *time.Time) {
	require.NoError(f.t, f.r.RefreshDay(context.Background(), snapshotDay, sample))
}
func (f *snapshotFixture) check(q string, want any) {
	f.t.Helper()
	switch v := want.(type) {
	case bool:
		var got bool
		require.NoError(f.t, f.c.QueryRowContext(context.Background(), q).Scan(&got))
		require.Equal(f.t, v, got)
	case int:
		var got int
		require.NoError(f.t, f.c.QueryRowContext(context.Background(), q).Scan(&got))
		require.Equal(f.t, v, got)
	}
}
func TestRefreshDay_SumsTotalCostAndModelsMatch(t *testing.T) {
	f := snapshotSetup(t, false)
	f.log(1, "a", ".1234567891")
	f.log(1, "b", ".2")
	f.refresh(nil)
	f.check(`SELECT total_cost=.3234567891 AND total_cost=(SELECT sum((value->>'total_cost')::numeric) FROM jsonb_each(models)) FROM supplier_usage_daily WHERE account_id=1`, true)
}
func TestRefreshDay_EmptyAndSpecialModelName(t *testing.T) {
	f := snapshotSetup(t, false)
	f.log(1, "", ".1")
	f.log(1, `a"\\b`, ".2")
	f.refresh(nil)
	f.check(`SELECT (SELECT count(*) FROM jsonb_each(models))=2 AND total_cost=(SELECT sum((value->>'total_cost')::numeric) FROM jsonb_each(models)) FROM supplier_usage_daily`, true)
}
func TestRefreshDay_SoftDeletedAccountStaysAttributed(t *testing.T) {
	f := snapshotSetup(t, false)
	f.exec(`UPDATE accounts SET deleted_at=now() WHERE id=1`)
	f.log(1, "m", "1")
	f.refresh(nil)
	f.check(`SELECT supplier_user_id=11 FROM supplier_usage_daily`, true)
}
func TestRefreshDay_IsolatesSuppliers(t *testing.T) {
	f := snapshotSetup(t, false)
	for _, id := range []int{1, 2, 3, 99} {
		f.log(id, "m", "1")
	}
	f.refresh(nil)
	f.check(`SELECT count(*) FROM supplier_usage_daily`, 2)
	f.check(`SELECT supplier_user_id=22 FROM supplier_usage_daily WHERE account_id=2`, true)
}
func TestRefreshDay_SampleWritesZeroUsageAccounts(t *testing.T) {
	f := snapshotSetup(t, false)
	f.exec(`UPDATE accounts SET deleted_at=now() WHERE id=2`)
	f.refresh(&snapshotDay)
	f.check(`SELECT count(*) FROM supplier_usage_daily`, 1)
	f.check(`SELECT requests=0 AND enabled AND sampled_at IS NOT NULL FROM supplier_usage_daily`, true)
}
func TestRefreshDay_NoSampleKeepsEnabled(t *testing.T) {
	f := snapshotSetup(t, false)
	f.refresh(&snapshotDay)
	f.exec(`UPDATE accounts SET supplier_paused=true`)
	f.refresh(nil)
	f.check(`SELECT bool_and(enabled AND sampled_at='2026-10-01'::timestamptz) FROM supplier_usage_daily`, true)
}
func TestRefreshDay_RejectsClosedDay(t *testing.T) {
	f := snapshotSetup(t, false)
	f.log(1, "m", "1")
	require.NoError(t, f.r.CloseDay(context.Background(), snapshotDay))
	f.log(1, "m", "2")
	require.ErrorIs(t, f.r.RefreshDay(context.Background(), snapshotDay, nil), service.ErrSupplierUsageDayClosed)
	f.check(`SELECT total_cost=1 FROM supplier_usage_daily`, true)
}
func TestClassification_Combinations(t *testing.T) {
	f := snapshotSetup(t, false)
	f.exec(`INSERT INTO accounts(id,supplier_user_id,name,status,supplier_paused,parent_account_id,temp_unschedulable_until) VALUES(4,11,'shadow','active',false,2,NULL),(5,11,'paused','active',true,NULL,NULL),(6,11,'disabled','disabled',false,NULL,NULL),(7,11,'error','error',true,NULL,NULL),(8,11,'ungrouped','active',false,NULL,NULL),(9,11,'cooling','active',false,NULL,now()+interval '1 hour'),(10,11,'both','active',true,NULL,now()+interval '1 hour');INSERT INTO account_groups VALUES(4),(5),(6),(7),(9),(10);UPDATE accounts SET supplier_paused=true WHERE id=2`)
	f.check(`SELECT count(*) FROM accounts a WHERE (`+SupplierAccountClassificationSQL+`)='enabled'`, 3)
	f.check(`SELECT count(*) FROM accounts a WHERE (`+SupplierAccountClassificationSQL+`)='pending'`, 6)
	f.check(`SELECT count(*) FROM accounts a WHERE (`+SupplierAccountClassificationSQL+`)='error'`, 1)
}
func TestCloseDay_InOrderOnly(t *testing.T) {
	f := snapshotSetup(t, false)
	f.log(1, "m", "1")
	ctx := context.Background()
	require.ErrorIs(t, f.r.CloseDay(ctx, snapshotDay.AddDate(0, 0, 1)), service.ErrSupplierUsageDayOutOfOrder)
	require.NoError(t, f.r.CloseDay(ctx, snapshotDay))
	f.log(1, "m", "1")
	require.NoError(t, f.r.CloseDay(ctx, snapshotDay))
	f.check(`SELECT total_cost=1 FROM supplier_usage_daily`, true)
	require.ErrorIs(t, f.r.CloseDay(ctx, snapshotDay.AddDate(0, 0, 2)), service.ErrSupplierUsageDayOutOfOrder)
	day, ok, e := f.r.FirstOpenDay(ctx)
	require.NoError(t, e)
	require.True(t, ok)
	require.Equal(t, snapshotDay.AddDate(0, 0, 1), day)
}
func TestCloseDay_KeepsLastSampleAndDoesNotResample(t *testing.T) {
	f := snapshotSetup(t, false)
	f.refresh(&snapshotDay)
	f.exec(`UPDATE accounts SET supplier_paused=true`)
	later := snapshotDay.Add(time.Hour)
	f.refresh(&later)
	f.exec(`UPDATE accounts SET supplier_paused=false;INSERT INTO accounts(id,supplier_user_id,name) VALUES(4,11,'new')`)
	f.log(4, "m", "1")
	require.NoError(t, f.r.CloseDay(context.Background(), snapshotDay))
	f.check(`SELECT NOT enabled AND sampled_at='2026-10-01 01:00Z' FROM supplier_usage_daily WHERE account_id=1`, true)
	f.check(`SELECT enabled IS NULL AND sampled_at IS NULL FROM supplier_usage_daily WHERE account_id=4`, true)
}
func TestCloseDay_ThenLogsDeleted_RowUnchanged(t *testing.T) {
	f := snapshotSetup(t, false)
	f.log(1, "m", "1")
	require.NoError(t, f.r.CloseDay(context.Background(), snapshotDay))
	f.exec(`DELETE FROM usage_logs`)
	require.NoError(t, f.r.CloseDay(context.Background(), snapshotDay))
	f.check(`SELECT total_cost=1 FROM supplier_usage_daily`, true)
}
func TestFindLateUsage_OnlyWhenLogsExceedFrozen(t *testing.T) {
	f := snapshotSetup(t, false)
	f.log(1, "m", "1")
	f.log(2, "m", "10")
	require.NoError(t, f.r.CloseDay(context.Background(), snapshotDay))
	f.exec(`DELETE FROM usage_logs WHERE account_id=2`)
	days, e := f.r.FindLateUsage(context.Background(), snapshotDay, snapshotDay.AddDate(0, 0, 1))
	require.NoError(t, e)
	require.Empty(t, days)
	f.log(1, "m", "2")
	days, e = f.r.FindLateUsage(context.Background(), snapshotDay, snapshotDay.AddDate(0, 0, 1))
	require.NoError(t, e)
	require.Len(t, days, 1)
}
func TestFindLateUsage_ReportsZeroCostLateRequest(t *testing.T) {
	f := snapshotSetup(t, false)
	f.log(1, "m", "0")
	require.NoError(t, f.r.CloseDay(context.Background(), snapshotDay))
	f.log(1, "m", "0")
	days, e := f.r.FindLateUsage(context.Background(), snapshotDay, snapshotDay.AddDate(0, 0, 1))
	require.NoError(t, e)
	require.Len(t, days, 1)
	require.Equal(t, int64(1), days[0].RequestDifference)
}
func TestSupplierUsageSnapshot_ResumeMatchesUninterrupted(t *testing.T) {
	for _, partition := range []bool{false, true} {
		t.Run(fmt.Sprint(partition), func(t *testing.T) {
			f := snapshotSetup(t, partition)
			f.log(1, "m", "1")
			for i := 0; i < 3; i++ {
				f.exec(`INSERT INTO usage_logs(account_id,model,total_cost,created_at) VALUES(1,'m',2,$1)`, snapshotDay.AddDate(0, 0, i))
			}
			ctx := context.Background()
			require.NoError(t, f.r.CloseDay(ctx, snapshotDay))
			f.r = &supplierUsageSnapshotRepository{db: f.c}
			for i := 1; i < 3; i++ {
				require.NoError(t, f.r.CloseDay(ctx, snapshotDay.AddDate(0, 0, i)))
			}
			f.exec(`CREATE TABLE resumed AS SELECT * FROM supplier_usage_daily;TRUNCATE supplier_usage_day_closes,supplier_usage_daily`)
			for i := 0; i < 3; i++ {
				require.NoError(t, f.r.CloseDay(ctx, snapshotDay.AddDate(0, 0, i)))
			}
			f.check(`SELECT NOT EXISTS((SELECT day,account_id,total_cost,models,enabled,sampled_at FROM resumed EXCEPT SELECT day,account_id,total_cost,models,enabled,sampled_at FROM supplier_usage_daily) UNION ALL (SELECT day,account_id,total_cost,models,enabled,sampled_at FROM supplier_usage_daily EXCEPT SELECT day,account_id,total_cost,models,enabled,sampled_at FROM resumed))`, true)
		})
	}
}

func TestSupplierUsageSnapshot_UTCWithNonUTCSession(t *testing.T) {
	f := snapshotSetup(t, false)
	f.exec(`SET TIME ZONE 'America/Los_Angeles'`)
	f.log(1, "m", "1")
	f.exec(`INSERT INTO usage_logs(account_id,model,total_cost,created_at) VALUES(1,'outside',10,'2026-09-30 23:59:59Z'),(1,'outside',10,'2026-10-02 00:00:00Z')`)
	f.refresh(nil)
	f.check(`SELECT day='2026-10-01'::date AND total_cost=1 FROM supplier_usage_daily`, true)
}

func TestFindLateUsage_ReportsIndividualMetricIncreases(t *testing.T) {
	for _, column := range []string{"total_cost", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens"} {
		t.Run(column, func(t *testing.T) {
			f := snapshotSetup(t, false)
			f.log(1, "m", "0")
			require.NoError(t, f.r.CloseDay(context.Background(), snapshotDay))
			f.exec("UPDATE usage_logs SET " + column + "=" + column + "+1")
			days, err := f.r.FindLateUsage(context.Background(), snapshotDay, snapshotDay.AddDate(0, 0, 1))
			require.NoError(t, err)
			require.Len(t, days, 1)
			require.Zero(t, days[0].RequestDifference)
		})
	}
}

func TestSupplierUsageSnapshot_RealMigratedSchema(t *testing.T) {
	ctx := context.Background()
	day := time.Date(1900, 1, 2, 0, 0, 0, 0, time.UTC)
	user := mustCreateUser(t, integrationEntClient, &service.User{Role: service.RoleSupplier})
	var accountID int64
	keyValue := fmt.Sprintf("sk-snapshot-real-%d", user.ID)
	t.Cleanup(func() {
		for _, statement := range []struct {
			query string
			arg   any
		}{
			{"DELETE FROM supplier_usage_daily WHERE day=$1", day},
			{"DELETE FROM supplier_usage_day_closes WHERE day=$1", day},
			{"DELETE FROM usage_logs WHERE user_id=$1", user.ID},
			{"DELETE FROM scheduler_outbox WHERE account_id=$1", accountID},
			{"DELETE FROM accounts WHERE id=$1", accountID},
			{"DELETE FROM api_keys WHERE user_id=$1", user.ID},
			{"DELETE FROM users WHERE id=$1", user.ID},
			{"DELETE FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to($1, 'UTF8')), 'hex')", keyValue},
		} {
			_, err := integrationDB.ExecContext(ctx, statement.query, statement.arg)
			require.NoError(t, err)
		}
	})
	key := mustCreateApiKey(t, integrationEntClient, &service.APIKey{UserID: user.ID, Key: keyValue})
	account, err := integrationEntClient.Account.Create().
		SetName("snapshot-real-schema").SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeOAuth).
		SetCredentials(map[string]any{}).SetSupplierUserID(user.ID).Save(ctx)
	require.NoError(t, err)
	accountID = account.ID
	insert := func(model, cost string, tokens int) {
		t.Helper()
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs
			(user_id,api_key_id,account_id,model,total_cost,input_tokens,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, user.ID, key.ID, accountID, model, cost, tokens, day.Add(time.Hour))
		require.NoError(t, err)
	}
	insert("model-a", "0.5", 15)
	insert(`model"b`, "1.25", 25)
	repo := NewSupplierUsageSnapshotRepository(integrationDB)
	sample := day.Add(2 * time.Hour)
	require.NoError(t, repo.RefreshDay(ctx, day, &sample))
	var owner, requests, tokens int64
	var total string
	var enabled bool
	var raw []byte
	err = integrationDB.QueryRowContext(ctx, `SELECT supplier_user_id,requests,input_tokens,total_cost::text,enabled,models
		FROM supplier_usage_daily WHERE day=$1 AND account_id=$2`, day, accountID).
		Scan(&owner, &requests, &tokens, &total, &enabled, &raw)
	require.NoError(t, err)
	require.Equal(t, user.ID, owner)
	require.EqualValues(t, 2, requests)
	require.EqualValues(t, 40, tokens)
	require.Equal(t, "1.7500000000", total)
	require.False(t, enabled, "an account without a group is pending")
	var models map[string]struct {
		Requests    int64  `json:"requests"`
		InputTokens int64  `json:"input_tokens"`
		TotalCost   string `json:"total_cost"`
	}
	require.NoError(t, json.Unmarshal(raw, &models))
	require.Len(t, models, 2)
	require.Equal(t, "0.5000000000", models["model-a"].TotalCost)
	require.Equal(t, "1.2500000000", models[`model"b`].TotalCost)
	require.EqualValues(t, 1, models["model-a"].Requests)
	require.EqualValues(t, 25, models[`model"b`].InputTokens)
	require.NoError(t, repo.CloseDay(ctx, day))
	late, err := repo.FindLateUsage(ctx, day, day.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.Empty(t, late)
	insert("model-a", "0.25", 9)
	late, err = repo.FindLateUsage(ctx, day, day.AddDate(0, 0, 1))
	require.NoError(t, err)
	require.Len(t, late, 1)
	require.Equal(t, day, late[0].Day)
	require.Equal(t, "0.2500000000", late[0].CostDifference)
	require.EqualValues(t, 1, late[0].RequestDifference)
	require.EqualValues(t, 9, late[0].InputTokenDifference)
}

// Use an isolated database with all production migrations; partition columns
// are copied from the migrated table, never redeclared in the fixture.
func cleanupGuardDB(t *testing.T, partition bool) *sql.DB {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("cleanup_guard_%d", time.Now().UnixNano())
	_, err := integrationDB.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	u, err := url.Parse(integrationDSN)
	require.NoError(t, err)
	u.Path = "/" + name
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = integrationDB.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	})
	require.NoError(t, ApplyMigrations(ctx, db))
	if partition {
		_, err = db.ExecContext(ctx, `
   ALTER TABLE usage_logs RENAME TO usage_logs_source;
   CREATE TABLE usage_logs (LIKE usage_logs_source INCLUDING DEFAULTS INCLUDING CONSTRAINTS) PARTITION BY RANGE(created_at);
   CREATE TABLE usage_logs_190001 PARTITION OF usage_logs FOR VALUES FROM ('1900-01-01') TO ('1900-02-01');

  `)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `
  INSERT INTO users(id,email,password_hash) VALUES(1,'guard@test.example','test');
  INSERT INTO api_keys(id,user_id,key,name) VALUES(1,1,'guard-key','guard');
  INSERT INTO accounts(id,name,platform,type,credentials) VALUES(1,'guard','openai','oauth','{}');
  INSERT INTO usage_logs(user_id,api_key_id,account_id,model,created_at) VALUES
   (1,1,1,'guard','1900-01-01 23:59:59+00'),
   (1,1,1,'guard','1900-01-02 00:00:00+00'),
   (1,1,1,'guard','1900-01-03 00:00:00+00');
 `)
	require.NoError(t, err)
	return db
}
func TestCleanupUsageLogs_CapsCutoffAtFirstOpenDay(t *testing.T) {
	db := cleanupGuardDB(t, false)
	_, err := db.Exec("INSERT INTO supplier_usage_day_closes(day,closed_at) VALUES('1900-01-01',now())")
	require.NoError(t, err)
	cutoff := time.Date(1900, 2, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, NewDashboardAggregationRepository(db, NewSupplierUsageSnapshotRepository(db)).CleanupUsageLogs(context.Background(), cutoff))
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_logs").Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_logs WHERE created_at < '1900-01-02'").Scan(&count))
	require.Zero(t, count)
}
func TestCleanupUsageLogs_NoClosedDays_DeletesNothing(t *testing.T) {
	db := cleanupGuardDB(t, false)
	require.NoError(t, NewDashboardAggregationRepository(db, NewSupplierUsageSnapshotRepository(db)).CleanupUsageLogs(context.Background(), time.Date(1900, 2, 1, 0, 0, 0, 0, time.UTC)))
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_logs").Scan(&count))
	require.Equal(t, 3, count)
}
func TestCleanupUsageLogs_PartitionWithOpenDayNotDropped(t *testing.T) {
	db := cleanupGuardDB(t, true)
	_, err := db.Exec("INSERT INTO supplier_usage_day_closes(day,closed_at) VALUES('1900-01-01',now())")
	require.NoError(t, err)
	require.NoError(t, NewDashboardAggregationRepository(db, NewSupplierUsageSnapshotRepository(db)).CleanupUsageLogs(context.Background(), time.Date(1900, 2, 1, 0, 0, 0, 0, time.UTC)))
	var exists bool
	require.NoError(t, db.QueryRow("SELECT to_regclass('usage_logs_190001') IS NOT NULL").Scan(&exists))
	require.True(t, exists)
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_logs").Scan(&count))
	require.Equal(t, 2, count)
}

func TestUsageCleanup_InclusiveEndKeepsOpenMidnight(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("previously_queued_%t", queued), func(t *testing.T) {
			db := cleanupGuardDB(t, false)
			ctx := context.Background()
			_, err := db.Exec("INSERT INTO supplier_usage_day_closes(day,closed_at) VALUES('1900-01-01',now())")
			require.NoError(t, err)
			day := time.Date(1900, 1, 2, 0, 0, 0, 0, time.UTC)
			filters := service.UsageCleanupFilters{StartTime: day.Add(-24 * time.Hour), EndTime: day.Add(-time.Nanosecond)}
			repo := NewUsageCleanupRepository(nil, db)
			var oldTask *service.UsageCleanupTask
			if queued {
				oldTask = &service.UsageCleanupTask{Status: service.UsageCleanupStatusPending, Filters: filters, CreatedBy: 1}
				require.NoError(t, repo.CreateTask(ctx, oldTask))
			}
			svc := service.NewUsageCleanupService(repo, nil, nil, &config.Config{UsageCleanup: config.UsageCleanupConfig{Enabled: true}}, NewSupplierUsageSnapshotRepository(db))
			defer svc.Stop()
			task, err := svc.CreateTask(ctx, filters, 1)
			require.NoError(t, err)
			if queued {
				task = oldTask
			}
			require.Eventually(t, func() bool {
				status, e := repo.GetTaskStatus(ctx, task.ID)
				return e == nil && status == service.UsageCleanupStatusSucceeded
			}, 5*time.Second, 10*time.Millisecond)
			var count int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_logs WHERE created_at = $1", day).Scan(&count))
			require.Equal(t, 1, count, "PostgreSQL must not round an inclusive end into the first open day")
			require.NoError(t, db.QueryRow("SELECT count(*) FROM usage_logs").Scan(&count))
			require.Equal(t, 2, count)
		})
	}
}
