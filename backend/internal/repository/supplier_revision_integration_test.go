//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestSupplierShadowProxyReplacementAndDetach(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	cache := NewSchedulerCache(testRedis(t))
	r := newSupplierRepository(client, tx, cache)
	ar := newAccountRepositoryWithSQL(client, tx, cache)
	pr := newProxyRepositoryWithSQL(client, tx)
	owner := int64(11)
	p1 := &service.Proxy{Name: "p1", Host: "8.8.8.8", Port: 80, Protocol: "http", Status: "active", SupplierUserID: &owner}
	require.NoError(t, pr.Create(ctx, p1))
	p2 := &service.Proxy{Name: "p2", Host: "8.8.4.4", Port: 80, Protocol: "http", Status: "active", SupplierUserID: &owner}
	require.NoError(t, pr.Create(ctx, p2))
	parent := &service.Account{Name: "parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: "active", SupplierUserID: &owner, ProxyID: &p1.ID, Credentials: map[string]any{"access_token": "old"}}
	require.NoError(t, ar.Create(ctx, parent))
	shadow := &service.Account{Name: "shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: "inactive", SupplierUserID: &owner, ParentAccountID: &parent.ID, QuotaDimension: service.QuotaDimensionSpark, ProxyID: &p1.ID, Priority: 99, Extra: map[string]any{"admin": "preserved"}}
	require.NoError(t, ar.Create(ctx, shadow))
	for _, binding := range []int64{p2.ID, 0} {
		got, err := r.UpdateAccount(ctx, owner, parent.ID, service.SupplierAccountUpdate{ProxyID: &binding, Credentials: map[string]any{"access_token": "new"}})
		require.NoError(t, err)
		require.Greater(t, got.GetCredentialAsInt64("_token_version"), parent.GetCredentialAsInt64("_token_version"))
		parent = got
		child, err := r.GetAccount(ctx, owner, shadow.ID)
		require.NoError(t, err)
		if binding == 0 {
			require.Nil(t, child.ProxyID)
		} else {
			require.Equal(t, binding, *child.ProxyID)
		}
		require.Equal(t, 99, child.Priority)
		require.Equal(t, "inactive", child.Status)
		require.Equal(t, "preserved", child.Extra["admin"])
		require.Equal(t, &owner, child.SupplierUserID)
		cached, err := cache.GetAccount(ctx, shadow.ID)
		require.NoError(t, err)
		require.Equal(t, child.ProxyID, cached.ProxyID)
		var count int
		rows, err := tx.QueryContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE payload @> jsonb_build_object('account_ids',jsonb_build_array($1::bigint))`, shadow.ID)
		require.NoError(t, err)
		require.True(t, rows.Next())
		require.NoError(t, rows.Scan(&count))
		require.NoError(t, rows.Close())
		require.Greater(t, count, 0)
	}
}

// Wait for a real PostgreSQL lock wait, rather than assuming a goroutine has reached its write.
func supplierWaitLocked(t *testing.T, pid int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := integrationDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
}
func supplierTxPID(t *testing.T, tx *sql.Tx) int {
	t.Helper()
	var pid int
	require.NoError(t, tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid))
	return pid
}

func TestSupplierLiveProxyReferenceSerialization(t *testing.T) {
	for _, field := range []string{"proxy_id", "proxy_fallback_origin_id", "backup_proxy_id"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			owner := int64(991122)
			r := newSupplierRepository(client, integrationDB, nil)
			pr := newProxyRepositoryWithSQL(client, integrationDB)
			ar := newAccountRepositoryWithSQL(client, integrationDB, nil)
			target := &service.Proxy{Name: "target", Host: "8.8.8.8", Protocol: "http", Port: 80, Status: "active", SupplierUserID: &owner}
			require.NoError(t, pr.Create(ctx, target))
			source := &service.Account{Name: "source", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: "active"}
			require.NoError(t, ar.Create(ctx, source))
			backup := &service.Proxy{Name: "backup", Host: "8.8.4.4", Protocol: "http", Port: 80, Status: "active"}
			require.NoError(t, pr.Create(ctx, backup))
			t.Cleanup(func() {
				_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, source.ID)
				_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, source.ID)
				_, _ = integrationDB.Exec(`DELETE FROM proxies WHERE id IN ($1,$2)`, backup.ID, target.ID)
			})
			table, id := "accounts", source.ID
			if field == "backup_proxy_id" {
				table, id = "proxies", backup.ID
			}
			query := fmt.Sprintf("UPDATE %s SET %s=$1 WHERE id=$2", table, field)
			// A binding that wins first protects the target through its transaction.
			writer, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer writer.Rollback()
			_, err = writer.ExecContext(ctx, query, target.ID, id)
			require.NoError(t, err)
			deleting, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer deleting.Rollback()
			pid := supplierTxPID(t, deleting)
			locked := make(chan error, 1)
			go func() {
				_, e := deleting.ExecContext(ctx, `LOCK TABLE accounts,proxies IN SHARE ROW EXCLUSIVE MODE`)
				locked <- e
			}()
			supplierWaitLocked(t, pid)
			require.NoError(t, writer.Commit())
			require.NoError(t, <-locked)
			// Repository reference guard runs inside the same deletion transaction and sees the committed writer.
			// Ent must share the transaction, so exercise the production count predicate directly here.
			var refs bool
			require.NoError(t, deleting.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE deleted_at IS NULL AND (proxy_id=$1 OR proxy_fallback_origin_id=$1)) OR EXISTS(SELECT 1 FROM proxies WHERE deleted_at IS NULL AND backup_proxy_id=$1)`, target.ID).Scan(&refs))
			require.True(t, refs)
			require.NoError(t, deleting.Rollback())
			require.ErrorIs(t, r.DeleteProxy(ctx, owner, target.ID), service.ErrSupplierProxyInUse)
			_, err = integrationDB.ExecContext(ctx, query, nil, id)
			require.NoError(t, err)
			// Deletion wins first: queued writes must recheck live state after the lock is released.
			deleting, err = integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer deleting.Rollback()
			_, err = deleting.ExecContext(ctx, `LOCK TABLE accounts,proxies IN SHARE ROW EXCLUSIVE MODE`)
			require.NoError(t, err)
			_, err = deleting.ExecContext(ctx, `UPDATE proxies SET deleted_at=NOW() WHERE id=$1`, target.ID)
			require.NoError(t, err)
			writer, err = integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer writer.Rollback()
			pid = supplierTxPID(t, writer)
			result := make(chan error, 1)
			go func() { _, e := writer.ExecContext(ctx, query, target.ID, id); result <- e }()
			supplierWaitLocked(t, pid)
			require.NoError(t, deleting.Commit())
			err = <-result
			require.Error(t, err)
			require.Contains(t, err.Error(), "live_proxy_reference")
			require.NoError(t, writer.Rollback())
		})
	}
}

func TestSupplierConcurrentCredentialVersions(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	owner := int64(992233)
	r := newSupplierRepository(client, integrationDB, nil)
	ar := newAccountRepositoryWithSQL(client, integrationDB, nil)
	initial := time.Now().Add(time.Hour).UnixMilli()
	a := &service.Account{Name: "version", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: "active", SupplierUserID: &owner, Priority: 99, Credentials: map[string]any{"access_token": "initial", "_token_version": initial}}
	require.NoError(t, ar.Create(ctx, a))
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, a.ID)
		_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, a.ID)
	})
	result := make(chan error, 2)
	for _, token := range []string{"one", "two"} {
		go func(token string) {
			_, err := r.UpdateAccount(ctx, owner, a.ID, service.SupplierAccountUpdate{Credentials: map[string]any{"access_token": token, "_token_version": int64(1)}})
			result <- err
		}(token)
	}
	require.NoError(t, <-result)
	require.NoError(t, <-result)
	got, err := r.GetAccount(ctx, owner, a.ID)
	require.NoError(t, err)
	require.Equal(t, initial+2, got.GetCredentialAsInt64("_token_version"))
	require.Equal(t, 99, got.Priority)
}

func TestSupplierLiveProxyGuardPreservesHistoryAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	pr := newProxyRepositoryWithSQL(client, tx)
	ar := newAccountRepositoryWithSQL(client, tx, nil)
	p := &service.Proxy{Name: "historic", Host: "8.8.8.8", Protocol: "http", Port: 80, Status: "active"}
	require.NoError(t, pr.Create(ctx, p))
	a := &service.Account{Name: "historic", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: "active", ProxyID: &p.ID, ProxyFallbackOriginID: &p.ID}
	require.NoError(t, ar.Create(ctx, a))
	backup := &service.Proxy{Name: "backup", Host: "8.8.4.4", Protocol: "http", Port: 80, Status: "active", BackupProxyID: &p.ID}
	require.NoError(t, pr.Create(ctx, backup))
	_, err := tx.ExecContext(ctx, `UPDATE proxies SET deleted_at=NOW() WHERE id=$1`, p.ID)
	require.NoError(t, err)
	raw, err := migrations.FS.ReadFile("244_live_proxy_references.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(raw))
		require.NoError(t, err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET name='unrelated' WHERE id=$1`, a.ID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE proxies SET name='unrelated' WHERE id=$1`, backup.ID)
	require.NoError(t, err)
	// A deleted source may retain history, but restoration validates its targets again.
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, a.ID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SAVEPOINT restoration`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET deleted_at=NULL WHERE id=$1`, a.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "live_proxy_reference")
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT restoration`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE proxies SET deleted_at=NOW() WHERE id=$1`, backup.ID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SAVEPOINT backup_restoration`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE proxies SET deleted_at=NULL WHERE id=$1`, backup.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "live_proxy_reference")
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT backup_restoration`)
	require.NoError(t, err)
}
