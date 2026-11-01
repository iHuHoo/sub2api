//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type overviewContract struct {
	Timezone  string                                         `json:"timezone"`
	Ready     bool                                           `json:"ready"`
	UpdatedAt *time.Time                                     `json:"updated_at"`
	Cost      struct{ Total, Today string }                  `json:"cost"`
	Tokens    struct{ Total, Today int64 }                   `json:"tokens"`
	Requests  struct{ Total, Today int64 }                   `json:"requests"`
	Accounts  struct{ Total, Enabled, Pending, Error int64 } `json:"accounts"`
	Daily     []struct {
		Date, Cost string
		Tokens     int64
		Enabled    *int64 `json:"enabled_accounts"`
	} `json:"daily"`
	PerAccount []struct {
		ID           int64
		Name, Status string
		Deleted      bool
		Today        string `json:"today_cost"`
		Last31       string `json:"last31_cost"`
		Total        string `json:"total_cost"`
	} `json:"per_account"`
}

type overviewFixture struct {
	t     *testing.T
	db    *sql.DB
	today time.Time
	raw   []byte
}

func overviewSetup(t *testing.T) *overviewFixture {
	t.Helper()
	f := &overviewFixture{t: t, db: cleanupGuardDB(t, false), today: time.Now().UTC().Truncate(24 * time.Hour)}
	f.exec(`DELETE FROM usage_logs; DELETE FROM accounts; INSERT INTO users(id,email,password_hash,role) VALUES(2,'other@test.example','test','supplier'); UPDATE users SET role='supplier' WHERE id=1;
 INSERT INTO accounts(id,name,platform,type,credentials,supplier_user_id) VALUES(1,'one','openai','oauth','{}',1),(2,'foreign','openai','oauth','{}',2),(3,'zero','openai','oauth','{}',1);
 INSERT INTO groups(id,name) VALUES(987,'overview'); INSERT INTO account_groups(account_id,group_id) VALUES(1,987);`)
	return f
}
func (f *overviewFixture) exec(q string, args ...any) {
	f.t.Helper()
	_, err := f.db.ExecContext(context.Background(), q, args...)
	require.NoError(f.t, err)
}
func (f *overviewFixture) snapshot(id, owner int, day time.Time, cost string) {
	f.t.Helper()
	f.exec(`INSERT INTO supplier_usage_daily(day,account_id,supplier_user_id,account_name,total_cost,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,updated_at) VALUES($1,$2,$3,'saved name',$4,2,10,20,30,40,$5)`, day, id, owner, cost, f.today.Add(10*time.Minute))
}
func (f *overviewFixture) get(owner int64, now time.Time) overviewContract {
	f.t.Helper()
	out, err := (&supplierRepository{sql: f.db}).usageOverview(context.Background(), owner, now)
	require.NoError(f.t, err)
	data, err := json.Marshal(out)
	require.NoError(f.t, err)
	f.raw = data
	var got overviewContract
	require.NoError(f.t, json.Unmarshal(data, &got))
	return got
}
func TestOverview_TotalEqualsSumOfPerAccount(t *testing.T) {
	f := overviewSetup(t)
	f.snapshot(1, 1, f.today, "0.1000000001")
	f.snapshot(3, 1, f.today.AddDate(0, 0, -30), "0.2")
	f.snapshot(1, 1, f.today.AddDate(0, 0, -31), "5")
	got := f.get(1, f.today)
	require.Equal(t, "5.3000000001", got.Cost.Total)
	require.Equal(t, "0.1000000001", got.Cost.Today)
	require.Len(t, got.PerAccount, 2)
	sum := new(big.Rat)
	for _, a := range got.PerAccount {
		n, ok := new(big.Rat).SetString(a.Total)
		require.True(t, ok)
		sum.Add(sum, n)
	}
	total, ok := new(big.Rat).SetString(got.Cost.Total)
	require.True(t, ok)
	require.Zero(t, sum.Cmp(total))
	require.Equal(t, int64(1), got.PerAccount[0].ID)
	require.Equal(t, "0.1000000001", got.PerAccount[0].Last31)
	require.Equal(t, "0.2000000000", got.PerAccount[1].Last31)
	require.NotNil(t, got.UpdatedAt)
	require.Equal(t, f.today.Add(10*time.Minute), *got.UpdatedAt)
	require.Equal(t, "0.2000000000", got.Daily[0].Cost)
	require.Equal(t, "0.1000000001", got.Daily[30].Cost)
}
func TestOverview_OnlyEverEnabledAccountsAreListed(t *testing.T) {
	f := overviewSetup(t)
	f.exec(`UPDATE accounts SET supplier_paused=true WHERE id IN (1,3);
 INSERT INTO accounts(id,name,platform,type,credentials,supplier_user_id,deleted_at) VALUES
 (4,'deleted never enabled','openai','oauth','{}',1,now()),
 (5,'deleted historic usage','openai','oauth','{}',1,now()),
 (6,'live historic usage','openai','oauth','{}',1,NULL),
 (7,'deleted enabled once','openai','oauth','{}',1,now())`)
	f.exec(`INSERT INTO supplier_usage_daily(day,account_id,supplier_user_id,account_name,enabled) VALUES
 ($1,1,1,'one',false),($1,3,1,'zero',true),($1,4,1,'deleted never enabled',false),
 ($1,7,1,'deleted enabled once',true),($2,3,1,'zero',false)`, f.today.AddDate(0, 0, -40), f.today)
	f.snapshot(5, 1, f.today, "0.005")
	f.snapshot(6, 1, f.today, "1.25")
	got := f.get(1, f.today)
	require.Equal(t, int64(3), got.Accounts.Total)
	require.Len(t, got.PerAccount, 4)
	require.Equal(t, []int64{3, 5, 6, 7}, []int64{got.PerAccount[0].ID, got.PerAccount[1].ID, got.PerAccount[2].ID, got.PerAccount[3].ID})
	require.Equal(t, "pending", got.PerAccount[0].Status)
	require.Equal(t, "0.0000000000", got.PerAccount[0].Total)
	require.True(t, got.PerAccount[1].Deleted)
	require.False(t, got.PerAccount[2].Deleted)
	require.True(t, got.PerAccount[3].Deleted)
	require.Equal(t, "1.2550000000", got.Cost.Total)
}
func TestOverview_AccountCountsSumToTotal(t *testing.T) {
	f := overviewSetup(t)
	f.exec(`INSERT INTO accounts(id,name,platform,type,credentials,supplier_user_id,status,supplier_paused,parent_account_id,quota_dimension,temp_unschedulable_until) VALUES
 (4,'error','openai','oauth','{}',1,'error',true,NULL,'global',NULL),
 (5,'paused','openai','oauth','{}',1,'active',true,NULL,'global',NULL),
 (6,'disabled','openai','oauth','{}',1,'disabled',false,NULL,'global',NULL),
 (7,'child','openai','oauth','{}',1,'active',false,5,'spark',NULL),
 (8,'cooling','openai','oauth','{}',1,'active',false,NULL,'global',now()+interval '1 hour'),
 (9,'both','openai','oauth','{}',1,'active',true,NULL,'global',now()+interval '1 hour');
 INSERT INTO account_groups(account_id,group_id) VALUES(4,987),(5,987),(6,987),(7,987),(8,987),(9,987)`)
	f.exec(`INSERT INTO supplier_usage_daily(day,account_id,supplier_user_id,account_name,enabled) SELECT $1,id,1,name,true FROM accounts WHERE supplier_user_id=1`, f.today)
	got := f.get(1, f.today)
	require.Equal(t, int64(8), got.Accounts.Total)
	require.Equal(t, int64(2), got.Accounts.Enabled)
	require.Equal(t, int64(5), got.Accounts.Pending)
	require.Equal(t, int64(1), got.Accounts.Error)
	require.Equal(t, got.Accounts.Total, got.Accounts.Enabled+got.Accounts.Pending+got.Accounts.Error)
	require.Len(t, got.PerAccount, 8)
	require.Equal(t, "enabled", got.PerAccount[0].Status)
	require.Equal(t, "pending", got.PerAccount[1].Status)
	require.Equal(t, "error", got.PerAccount[2].Status)
}
func TestOverview_DeletedAccountKeptInPerAccountNotInCount(t *testing.T) {
	f := overviewSetup(t)
	f.snapshot(1, 1, f.today, "12")
	f.exec(`UPDATE accounts SET deleted_at=now() WHERE id=1`)
	f.snapshot(99, 1, f.today.AddDate(0, 0, -2), "3")
	got := f.get(1, f.today)
	require.Equal(t, int64(1), got.Accounts.Total)
	require.Len(t, got.PerAccount, 2)
	require.True(t, got.PerAccount[0].Deleted)
	require.Equal(t, "one", got.PerAccount[0].Name)
	require.Equal(t, "12.0000000000", got.PerAccount[0].Total)
	require.Equal(t, int64(99), got.PerAccount[1].ID)
	require.True(t, got.PerAccount[1].Deleted)
	require.Equal(t, "saved name", got.PerAccount[1].Name)
	require.Equal(t, "15.0000000000", got.Cost.Total)
}
func TestOverview_IsolatesSuppliers(t *testing.T) {
	f := overviewSetup(t)
	f.snapshot(1, 1, f.today, "1")
	f.snapshot(2, 2, f.today, "999")
	got := f.get(1, f.today)
	require.Equal(t, "1.0000000000", got.Cost.Total)
	require.Equal(t, int64(2), got.Requests.Total)
	require.Len(t, got.PerAccount, 1)
	require.Equal(t, int64(1), got.PerAccount[0].ID)
	foreign := f.get(2, f.today)
	require.Equal(t, "999.0000000000", foreign.Cost.Total)
	require.Len(t, foreign.PerAccount, 1)
}
func TestOverview_NoLeakedFields(t *testing.T) {
	f := overviewSetup(t)
	f.snapshot(1, 1, f.today, "2")
	got := f.get(1, f.today)
	require.Equal(t, "2.0000000000", got.Cost.Total)
	data := f.raw
	for _, private := range []string{"actual_cost", "account_cost", "api_key", "user_id"} {
		require.NotContains(t, string(data), private)
	}
}
func TestOverview_NewSupplierReturnsZeroesAnd31Days(t *testing.T) {
	f := overviewSetup(t)
	got := f.get(999, f.today)
	require.True(t, got.Ready)
	require.Equal(t, "UTC", got.Timezone)
	require.Equal(t, "0", got.Cost.Total)
	require.Equal(t, "0", got.Cost.Today)
	require.Zero(t, got.Requests.Total)
	require.Zero(t, got.Tokens.Total)
	require.Zero(t, got.Accounts.Total)
	require.Nil(t, got.UpdatedAt)
	require.NotNil(t, got.PerAccount)
	require.Empty(t, got.PerAccount)
	require.Len(t, got.Daily, 31)
	require.Equal(t, f.today.AddDate(0, 0, -30).Format("2006-01-02"), got.Daily[0].Date)
	require.Equal(t, f.today.Format("2006-01-02"), got.Daily[30].Date)
	for _, d := range got.Daily {
		require.Equal(t, "0", d.Cost)
		require.Zero(t, d.Tokens)
		require.Nil(t, d.Enabled)
	}
}
func TestOverview_NotReadyDuringBackfill(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hour      int
		minute    int
		closedAgo int
		usageAgo  int
		ready     bool
	}{
		{"00:30 grace", 0, 30, 2, 0, true}, {"two hour midnight delay", 2, 0, 2, 0, true}, {"partial first open today minus two", 2, 0, 3, 0, false},
		{"only yesterday no closes", 0, 30, 0, 1, true}, {"only today no closes", 2, 0, 0, 0, true}, {"old usage no closes", 2, 0, 0, 2, false}, {"no usage no closes", 2, 0, 0, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := overviewSetup(t)
			wantCost := "0"
			if tc.closedAgo == 3 {
				f.snapshot(1, 1, f.today.AddDate(0, 0, -3), "0.5")
				wantCost = "0.5000000000"
			}
			if tc.closedAgo > 0 {
				f.exec(`INSERT INTO supplier_usage_day_closes(day) VALUES($1)`, f.today.AddDate(0, 0, -tc.closedAgo))
			}
			if tc.usageAgo >= 0 {
				f.exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,model,created_at,total_cost) VALUES(1,1,2,'private', $1,999)`, f.today.AddDate(0, 0, -tc.usageAgo))
			}
			got := f.get(1, f.today.Add(time.Duration(tc.hour)*time.Hour+time.Duration(tc.minute)*time.Minute))
			require.Equal(t, tc.ready, got.Ready)
			require.Equal(t, wantCost, got.Cost.Total)
		})
	}
}
func TestOverview_EnabledNullWhenNeverSampled(t *testing.T) {
	f := overviewSetup(t)
	f.snapshot(1, 1, f.today, "0")
	got := f.get(1, f.today)
	require.Len(t, got.Daily, 31)
	require.Nil(t, got.Daily[30].Enabled)
	f.exec(`UPDATE supplier_usage_daily SET enabled=false,sampled_at=$1`, f.today)
	got = f.get(1, f.today)
	require.NotNil(t, got.Daily[30].Enabled)
	require.Zero(t, *got.Daily[30].Enabled)
	f.snapshot(3, 1, f.today, "0")
	f.exec(`UPDATE supplier_usage_daily SET enabled=true,sampled_at=$1 WHERE account_id=3`, f.today)
	got = f.get(1, f.today)
	require.Equal(t, int64(1), *got.Daily[30].Enabled)
}
func TestOverview_TokensAreSumOfFour(t *testing.T) {
	f := overviewSetup(t)
	f.snapshot(1, 1, f.today, "0")
	f.snapshot(1, 1, f.today.AddDate(0, 0, -1), "0")
	got := f.get(1, f.today)
	require.Equal(t, int64(200), got.Tokens.Total)
	require.Equal(t, int64(100), got.Tokens.Today)
	require.Equal(t, int64(100), got.Daily[30].Tokens)
	require.Equal(t, int64(4), got.Requests.Total)
	require.Equal(t, int64(2), got.Requests.Today)
}
func TestOverview_ReadinessExistencePlan(t *testing.T) {
	f := overviewSetup(t)
	f.exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,model,created_at) SELECT 1,1,1,'plan', $1::timestamptz + n * interval '1 second' FROM generate_series(1,20000) n`, f.today)
	f.exec(`ANALYZE usage_logs`)
	rows, err := f.db.QueryContext(context.Background(), `EXPLAIN (ANALYZE,BUFFERS) SELECT EXISTS(SELECT 1 FROM usage_logs WHERE created_at<$1)`, f.today.AddDate(0, 0, -1))
	require.NoError(t, err)
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		lines = append(lines, line)
	}
	require.NoError(t, rows.Err())
	plan := strings.Join(lines, "\n")
	t.Log(plan)
	require.Contains(t, plan, "Index")
	require.Contains(t, plan, "created_at <")
}
