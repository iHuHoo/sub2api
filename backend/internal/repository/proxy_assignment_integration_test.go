//go:build integration

package repository

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestProxyAssignmentCASAndReferences(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	c := tx.Client()
	r := newProxyRepositoryWithSQL(c, tx)
	u, err := c.User.Create().SetEmail(fmt.Sprintf("assignment-%d@test.invalid", 1)).SetPasswordHash("unused").SetRole("supplier").SetStatus("active").Save(ctx)
	require.NoError(t, err)
	p := &service.Proxy{Name: "assigned", Protocol: "http", Host: "admin.internal", Port: 8080, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	got, err := r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.NoError(t, err)
	require.Equal(t, &u.ID, got.AssignedSupplierUserID)
	observed, err := r.AssignSupplier(ctx, p.ID, nil, nil)
	require.ErrorIs(t, err, service.ErrProxyAssignmentConflict)
	require.Equal(t, &u.ID, observed.AssignedSupplierUserID)
	_, err = c.Account.Create().SetName("foreign").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetProxyID(p.ID).Save(ctx)
	require.Error(t, err)
}

func TestProxyAssignmentFirstAssignmentAndRevoke(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	c := tx.Client()
	r := newProxyRepositoryWithSQL(c, tx)
	u, err := c.User.Create().SetEmail("assignment-first@test.invalid").SetPasswordHash("unused").SetRole("supplier").Save(ctx)
	require.NoError(t, err)
	p := &service.Proxy{Name: "first", Protocol: "http", Host: "private.admin", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	a, err := c.Account.Create().SetName("existing").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetSupplierUserID(u.ID).SetProxyID(p.ID).Save(ctx)
	require.NoError(t, err)
	got, err := r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.NoError(t, err)
	require.True(t, got.AssignmentValid)
	var assignmentEvents int
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2`, []any{a.ID, service.SchedulerOutboxEventAccountChanged}, &assignmentEvents))
	require.Positive(t, assignmentEvents, "first assignment refreshes existing bound accounts without probe metadata")
	_, err = r.AssignSupplier(ctx, p.ID, &u.ID, &u.ID)
	require.NoError(t, err)
	_, err = r.AssignSupplier(ctx, p.ID, &u.ID, nil)
	require.ErrorIs(t, err, service.ErrProxyAssignmentConflict)
	sr := newSupplierRepository(c, tx, nil)
	visible, err := sr.GetProxy(ctx, u.ID, p.ID)
	require.NoError(t, err)
	require.True(t, visible.AccessibleToSupplier(u.ID))
	_, err = sr.GetProxy(ctx, u.ID+1000, p.ID)
	require.ErrorIs(t, err, service.ErrSupplierUnavailable)
	_, err = c.Account.UpdateOneID(a.ID).ClearProxyID().Save(ctx)
	require.NoError(t, err)
	_, err = r.AssignSupplier(ctx, p.ID, &u.ID, nil)
	require.NoError(t, err)
	_, err = sr.GetProxy(ctx, u.ID, p.ID)
	require.ErrorIs(t, err, service.ErrSupplierUnavailable)
}
func TestProxyAssignmentRejectsInvalidTargetAndCreator(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	c := tx.Client()
	r := newProxyRepositoryWithSQL(c, tx)
	u, err := c.User.Create().SetEmail("assignment-disabled@test.invalid").SetPasswordHash("unused").SetRole("supplier").SetStatus("disabled").Save(ctx)
	require.NoError(t, err)
	p := &service.Proxy{Name: "invalid", Protocol: "http", Host: "private.admin", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.ErrorIs(t, err, service.ErrProxyAssignmentInvalid)
	missing := int64(999999)
	_, err = r.AssignSupplier(ctx, p.ID, nil, &missing)
	require.ErrorIs(t, err, service.ErrProxyAssignmentInvalid)
	_, err = c.User.UpdateOneID(u.ID).SetStatus("active").SetRole("user").Save(ctx)
	require.NoError(t, err)
	_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.ErrorIs(t, err, service.ErrProxyAssignmentInvalid)
	p.SupplierUserID = &u.ID
	p.ID = 0
	require.NoError(t, r.Create(ctx, p))
	_, err = r.AssignSupplier(ctx, p.ID, nil, nil)
	require.ErrorIs(t, err, service.ErrProxyAssignmentInvalid)
}
func TestProxyAssignmentConcurrentCAS(t *testing.T) {
	ctx := context.Background()
	c := testEntClient(t)
	r := newProxyRepositoryWithSQL(c, integrationDB)
	u, err := c.User.Create().SetEmail(fmt.Sprintf("assignment-race-%d@test.invalid", time.Now().UnixNano())).SetPasswordHash("unused").SetRole("supplier").Save(ctx)
	require.NoError(t, err)
	p := &service.Proxy{Name: "race", Protocol: "http", Host: "private.admin", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM proxies WHERE id=$1", p.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id=$1", u.ID)
	})
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; _, e := r.AssignSupplier(ctx, p.ID, nil, &u.ID); errs <- e }()
	}
	close(start)
	first, second := <-errs, <-errs
	if first == nil {
		require.ErrorIs(t, second, service.ErrProxyAssignmentConflict)
	} else {
		require.ErrorIs(t, first, service.ErrProxyAssignmentConflict)
		require.NoError(t, second)
	}
}

func TestProxyAssignmentBindingRevokeRace(t *testing.T) {
	ctx := context.Background()
	c := testEntClient(t)
	r := newProxyRepositoryWithSQL(c, integrationDB)
	sr := newSupplierRepository(c, integrationDB, nil)
	u, err := c.User.Create().SetEmail(fmt.Sprintf("assignment-bind-%d@test.invalid", time.Now().UnixNano())).SetPasswordHash("unused").SetRole("supplier").Save(ctx)
	require.NoError(t, err)
	p := &service.Proxy{Name: "bind-race", Protocol: "http", Host: "private.admin", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.NoError(t, err)
	a := &service.Account{Name: "race", Platform: "openai", Type: "apikey", Credentials: map[string]any{"api_key": "test"}, ProxyID: &p.ID, Status: service.StatusActive}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE supplier_user_id=$1", u.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM proxies WHERE id=$1", p.ID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id=$1", u.ID)
	})
	start := make(chan struct{})
	binding := make(chan error, 1)
	revoke := make(chan error, 1)
	go func() { <-start; binding <- sr.CreateAccount(ctx, u.ID, a) }()
	go func() { <-start; _, e := r.AssignSupplier(ctx, p.ID, &u.ID, nil); revoke <- e }()
	close(start)
	bindErr, revokeErr := <-binding, <-revoke
	if bindErr == nil {
		require.ErrorIs(t, revokeErr, service.ErrProxyAssignmentConflict)
	} else {
		require.ErrorIs(t, bindErr, service.ErrSupplierUnavailable)
		require.NoError(t, revokeErr)
	}
}
func TestProxyAssignmentDatabaseRejectsCrossBackup(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	c := tx.Client()
	r := newProxyRepositoryWithSQL(c, tx)
	a := int64(11)
	b := int64(22)
	p := &service.Proxy{Name: "owned", SupplierUserID: &a, Protocol: "http", Host: "8.8.8.8", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	_, err := c.Proxy.Create().SetName("foreign").SetSupplierUserID(b).SetProtocol("http").SetHost("8.8.4.4").SetPort(80).SetBackupProxyID(p.ID).Save(ctx)
	require.Error(t, err)
}

func TestProxyAssignmentExpiryRechecksSupplierAfterSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	c := tx.Client()
	r := newProxyRepositoryWithSQL(c, tx)
	u, err := c.User.Create().SetEmail("assignment-expiry@test.invalid").SetPasswordHash("unused").SetRole("supplier").Save(ctx)
	require.NoError(t, err)
	p := &service.Proxy{Name: "expires", Protocol: "http", Host: "private.admin", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.NoError(t, err)
	b := &service.Proxy{Name: "backup", SupplierUserID: &u.ID, Protocol: "http", Host: "8.8.8.8", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, b))
	past := time.Now().Add(-time.Hour)
	_, err = c.Proxy.UpdateOneID(p.ID).SetExpiresAt(past).SetFallbackMode(service.FallbackModeProxy).SetBackupProxyID(b.ID).Save(ctx)
	require.NoError(t, err)
	a, err := c.Account.Create().SetName("expires").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetSupplierUserID(u.ID).SetProxyID(p.ID).Save(ctx)
	require.NoError(t, err)
	snapshot, err := r.GetByID(ctx, p.ID)
	require.NoError(t, err)
	require.True(t, snapshot.AssignmentValid)
	_, err = c.User.UpdateOneID(u.ID).SetStatus("disabled").Save(ctx)
	require.NoError(t, err)
	_, err = r.sweepOneExpiredProxyOnExec(ctx, tx, *snapshot, time.Now(), &b.ID, true)
	require.NoError(t, err)
	got, err := c.Account.Get(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, &p.ID, got.ProxyID)
}
func TestProxyAssignmentRejectsForeignDerivedReference(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	c := tx.Client()
	r := newProxyRepositoryWithSQL(c, tx)
	u, err := c.User.Create().SetEmail("assignment-derived@test.invalid").SetPasswordHash("unused").SetRole("supplier").Save(ctx)
	require.NoError(t, err)
	p := &service.Proxy{Name: "derived", Protocol: "http", Host: "admin.internal", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	parent, err := c.Account.Create().SetName("parent").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetSupplierUserID(u.ID).SetProxyID(p.ID).Save(ctx)
	require.NoError(t, err)
	_, err = c.Account.Create().SetName("foreign-child").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetParentAccountID(parent.ID).SetQuotaDimension(service.QuotaDimensionSpark).Save(ctx)
	require.NoError(t, err)
	_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.ErrorIs(t, err, service.ErrProxyAssignmentConflict)
}
func TestProxyAssignmentDatabaseRejectsForeignDerivedBinding(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	c := tx.Client()
	r := newProxyRepositoryWithSQL(c, tx)
	u, err := c.User.Create().SetEmail("assignment-derived-bind@test.invalid").SetPasswordHash("unused").SetRole("supplier").Save(ctx)
	require.NoError(t, err)
	p := &service.Proxy{Name: "derived-bound", Protocol: "http", Host: "admin.internal", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.NoError(t, err)
	parent, err := c.Account.Create().SetName("parent").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetSupplierUserID(u.ID).SetProxyID(p.ID).Save(ctx)
	require.NoError(t, err)
	_, err = c.Account.Create().SetName("foreign-child").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetParentAccountID(parent.ID).SetQuotaDimension(service.QuotaDimensionSpark).Save(ctx)
	require.Error(t, err)
}
func TestProxyAssignmentAdminAccountWritesReturnValidation(t *testing.T) {
	for _, op := range []string{"create", "update", "bulk"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			c := tx.Client()
			r := newProxyRepositoryWithSQL(c, tx)
			ar := newAccountRepositoryWithSQL(c, tx, nil)
			u, err := c.User.Create().SetEmail("assignment-write@test.invalid").SetPasswordHash("unused").SetRole("supplier").Save(ctx)
			require.NoError(t, err)
			p := &service.Proxy{Name: "exclusive", Protocol: "http", Host: "admin.internal", Port: 80, Status: service.StatusActive}
			require.NoError(t, r.Create(ctx, p))
			_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
			require.NoError(t, err)
			a := &service.Account{Name: "admin-owned", Platform: "openai", Type: "apikey", Status: service.StatusActive, Credentials: map[string]any{"api_key": "fixture"}}
			if op == "create" {
				a.ProxyID = &p.ID
				err = ar.CreateWithAccountGroups(ctx, a, nil)
			} else {
				require.NoError(t, ar.Create(ctx, a))
				if op == "update" {
					a.ProxyID = &p.ID
					err = ar.Update(ctx, a)
				} else {
					_, err = ar.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{ProxyID: &p.ID})
				}
			}
			require.ErrorIs(t, err, service.ErrProxyAssignmentInvalid)
		})
	}
}
func TestProxyAssignmentDerivedParentBindingSerialization(t *testing.T) {
	ctx := context.Background()
	c := testEntClient(t)
	r := newProxyRepositoryWithSQL(c, integrationDB)
	var userID, proxyID, parentID, childID int64
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", childID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", parentID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM proxies WHERE id=$1", proxyID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id=$1", userID)
	})
	u, err := c.User.Create().SetEmail(fmt.Sprintf("assignment-parent-race-%d@test.invalid", time.Now().UnixNano())).SetPasswordHash("unused").SetRole("supplier").Save(ctx)
	require.NoError(t, err)
	userID = u.ID
	p := &service.Proxy{Name: "parent-race", Protocol: "http", Host: "admin.internal", Port: 80, Status: service.StatusActive}
	require.NoError(t, r.Create(ctx, p))
	proxyID = p.ID
	_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.NoError(t, err)
	parent, err := c.Account.Create().SetName("parent").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetSupplierUserID(u.ID).Save(ctx)
	require.NoError(t, err)
	parentID = parent.ID
	childTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer childTx.Rollback()
	require.NoError(t, childTx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,parent_account_id,quota_dimension) VALUES('foreign-child','openai','apikey','{}',$1,'spark') RETURNING id`, parent.ID).Scan(&childID))
	parentTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer parentTx.Rollback()
	pid := supplierTxPID(t, parentTx)
	done := make(chan error, 1)
	go func() {
		_, e := parentTx.ExecContext(ctx, "UPDATE accounts SET proxy_id=$1 WHERE id=$2", p.ID, parent.ID)
		done <- e
	}()
	early := false
	var earlyErr error
	require.Eventually(t, func() bool {
		select {
		case earlyErr = <-done:
			early = true
			return true
		default:
		}
		var waiting bool
		e := integrationDB.QueryRowContext(ctx, `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting)
		return e == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
	if early {
		require.NoError(t, earlyErr)
		require.NoError(t, parentTx.Commit())
		require.NoError(t, childTx.Commit())
		t.Fatal("ownership violation: parent assignment and foreign derived reference both committed")
	}
	require.NoError(t, childTx.Commit())
	require.Error(t, <-done)
	require.NoError(t, parentTx.Rollback())
}

func TestAssignedProxyExpiryFallbackIntegration(t *testing.T) {
	for _, backup := range []bool{true, false} {
		t.Run(fmt.Sprintf("backup=%v", backup), func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			c := tx.Client()
			r := newProxyRepositoryWithSQL(c, tx)
			u, err := c.User.Create().SetEmail(fmt.Sprintf("expiry-%v@test.invalid", backup)).SetPasswordHash("unused").SetRole("supplier").SetStatus("active").Save(ctx)
			require.NoError(t, err)
			p := &service.Proxy{Name: "expired-assigned", Protocol: "http", Host: "private.internal", Port: 80, Status: service.StatusActive}
			require.NoError(t, r.Create(ctx, p))
			_, err = r.AssignSupplier(ctx, p.ID, nil, &u.ID)
			require.NoError(t, err)
			wantID := p.ID
			if backup {
				b := &service.Proxy{Name: "same-owner-backup", Protocol: "http", Host: "backup.internal", Port: 80, Status: service.StatusActive, SupplierUserID: &u.ID}
				require.NoError(t, r.Create(ctx, b))
				_, err = c.Proxy.UpdateOneID(p.ID).SetFallbackMode(service.FallbackModeProxy).SetBackupProxyID(b.ID).Save(ctx)
				require.NoError(t, err)
				wantID = b.ID
			}
			a, err := c.Account.Create().SetName("expiry-account").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetSupplierUserID(u.ID).SetProxyID(p.ID).SetSchedulable(true).Save(ctx)
			require.NoError(t, err)
			_, err = c.Proxy.UpdateOneID(p.ID).SetExpiresAt(time.Now().Add(-time.Hour)).Save(ctx)
			require.NoError(t, err)
			_, err = r.SweepExpiredProxies(ctx, time.Now())
			require.NoError(t, err)
			fresh, err := newAccountRepositoryWithSQL(c, tx, nil).GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.NotNil(t, fresh.ProxyID, "expiry must never enable direct egress")
			require.Equal(t, wantID, *fresh.ProxyID)
			require.Equal(t, backup, fresh.IsSchedulable())
			var events int
			require.NoError(t, scanSingleRow(ctx, tx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 OR payload @> jsonb_build_object('account_ids',jsonb_build_array($1::bigint))`, []any{a.ID}, &events))
			require.Positive(t, events, "expiry refreshes the bound account even without a fallback")
		})
	}
}
