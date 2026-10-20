//go:build integration

package repository

import (
	"context"
	"database/sql"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/url"
	"testing"
	"time"
)

// These checks must work even when lifecycle revocation did not run.
// No parallel tests while the table trigger is disabled.
func TestClashSubscriptionDownloadOwnerDefense(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `ALTER TABLE users DISABLE TRIGGER users_revoke_proxy_subscription`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `ALTER TABLE users ENABLE TRIGGER users_revoke_proxy_subscription`)
		require.NoError(t, err)
	})
	for _, change := range []struct{ name, update string }{
		{"disabled", `status='disabled'`},
		{"deleted", `deleted_at=now()`},
		// A different allowed role independently exercises owner_role equality.
		{"different-owner-role", `role='admin'`},
		{"non-owner-role", `role='user'`},
	} {
		t.Run(change.name, func(t *testing.T) {
			u := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleSupplier})
			t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, u.ID) })
			hash := "defense-" + change.name
			_, err := integrationDB.ExecContext(ctx, `INSERT INTO proxy_subscriptions(user_id,owner_role,token_hash,expires_at) VALUES($1,'supplier',$2,now()+interval '90 days')`, u.ID, hash)
			require.NoError(t, err)
			repo := NewClashSubscriptionRepository(integrationDB)
			read := func() error {
				return repo.ReadDownloadSnapshot(ctx, hash, func(*service.ProxySubscription, []service.Proxy) error { return nil })
			}
			require.NoError(t, read())
			_, err = integrationDB.ExecContext(ctx, `UPDATE users SET `+change.update+` WHERE id=$1`, u.ID)
			require.NoError(t, err)
			var version int64
			var revoked sql.NullTime
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT version,revoked_at FROM proxy_subscriptions WHERE user_id=$1`, u.ID).Scan(&version, &revoked))
			require.EqualValues(t, 1, version)
			require.False(t, revoked.Valid, "trigger must not mask download predicates")
			require.ErrorIs(t, read(), service.ErrClashNotFound)
		})
	}
}

func TestClashSubscriptionMetadataDoesNotLockOwner(t *testing.T) {
	ctx := context.Background()
	u := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleSupplier})
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM users WHERE id=$1`, u.ID) })
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`UPDATE users SET balance=balance+1 WHERE id=$1`, u.ID)
	require.NoError(t, err)
	// Billing holds the row lock. Reading metadata must not wait for it.
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	svc := service.NewClashSubscriptionService(NewClashSubscriptionRepository(integrationDB), &config.Config{ClashSubscription: config.ClashSubscriptionConfig{Enabled: true}})
	_, err = svc.GetMetadata(readCtx, service.SubscriptionActor{UserID: u.ID, Role: u.Role})
	require.NoError(t, err)
}

func TestClashSubscriptionRevocationTrigger(t *testing.T) {
	ctx := context.Background()
	u := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleSupplier})
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, u.ID) })
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO proxy_subscriptions(user_id,owner_role,token_hash,expires_at) VALUES($1,'supplier','fixture-hash',now()+interval '90 days')`, u.ID)
	require.NoError(t, err)
	read := func(want int64, revoked bool) {
		var version int64
		var at sql.NullTime
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT version,revoked_at FROM proxy_subscriptions WHERE user_id=$1`, u.ID).Scan(&version, &at))
		require.Equal(t, want, version)
		require.Equal(t, revoked, at.Valid)
	}
	exec := func(query string) { _, e := integrationDB.ExecContext(ctx, query, u.ID); require.NoError(t, e) }
	read(1, false)
	exec(`UPDATE users SET balance=balance+1, role=role, status=status WHERE id=$1`)
	read(1, false)
	exec(`UPDATE users SET status='disabled' WHERE id=$1`)
	read(2, true)
	exec(`UPDATE users SET status='active' WHERE id=$1`)
	read(2, true)
	exec(`UPDATE users SET role='admin',status='disabled',deleted_at=now() WHERE id=$1`)
	read(3, true)
	exec(`UPDATE users SET role='supplier',status='active',deleted_at=NULL WHERE id=$1`)
	read(4, true)
	exec(`DELETE FROM users WHERE id=$1`)
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM proxy_subscriptions WHERE user_id=$1`, u.ID).Scan(&n))
	require.Zero(t, n)
}

func TestClashSubscriptionScopeAndLifecycle(t *testing.T) {
	ctx := context.Background()
	admin := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleAdmin})
	a := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleSupplier})
	b := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleSupplier})
	var ids []int64
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = integrationDB.Exec(`DELETE FROM proxies WHERE id=$1`, id)
		}
		for _, u := range []*service.User{admin, a, b} {
			_, _ = integrationDB.Exec(`DELETE FROM users WHERE id=$1`, u.ID)
		}
	})
	add := func(owner, assigned *int64, status string, expired, deleted bool) {
		var id int64
		err := integrationDB.QueryRow(`INSERT INTO proxies(name,protocol,host,port,status,supplier_user_id,assigned_supplier_user_id,expires_at,deleted_at) VALUES('fixture','http','example.test',8080,$1,$2,$3,CASE WHEN $4 THEN now()-interval '1 hour' END,CASE WHEN $5 THEN now() END) RETURNING id`, status, owner, assigned, expired, deleted).Scan(&id)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	add(nil, nil, "active", false, false)
	add(nil, &a.ID, "active", false, false)
	add(nil, &b.ID, "active", false, false)
	add(&a.ID, nil, "active", false, false)
	add(nil, &a.ID, "inactive", false, false)
	add(nil, &a.ID, "active", true, false)
	add(nil, &a.ID, "active", false, true)
	repo := NewClashSubscriptionRepository(integrationDB)
	svc := service.NewClashSubscriptionService(repo, &config.Config{ClashSubscription: config.ClashSubscriptionConfig{Enabled: true, PublicBaseURL: "https://api.example"}})
	actor := service.SubscriptionActor{UserID: a.ID, Role: a.Role}
	for _, u := range []*service.User{admin, a, b} {
		m, e := svc.GetMetadata(ctx, service.SubscriptionActor{UserID: u.ID, Role: u.Role})
		require.NoError(t, e)
		require.Equal(t, 1, m.ExportableCount)
	}
	grant, e := svc.Create(ctx, actor, 30)
	require.NoError(t, e)
	parsed, e := url.Parse(grant.SubscriptionURL)
	require.NoError(t, e)
	token := parsed.Query().Get("token")
	_, set, e := svc.Snapshot(ctx, token)
	require.NoError(t, e)
	require.Len(t, set.Proxies, 1)
	require.Equal(t, ids[1], set.Proxies[0].ID)
	_, e = svc.Create(ctx, actor, 30)
	require.ErrorIs(t, e, service.ErrClashConflict)
	_, e = svc.Reset(ctx, actor, 99, 30)
	require.ErrorIs(t, e, service.ErrClashConflict)
	_, e = integrationDB.Exec(`UPDATE proxies SET assigned_supplier_user_id=NULL WHERE id=$1`, ids[1])
	require.NoError(t, e)
	_, e = svc.Reset(ctx, actor, 1, 30)
	require.ErrorIs(t, e, service.ErrClashNoProxies)
	_, set, e = svc.Snapshot(ctx, token)
	require.NoError(t, e)
	require.Empty(t, set.Proxies)
	require.NoError(t, svc.Revoke(ctx, actor, 1))
	_, _, e = svc.Snapshot(ctx, token)
	require.ErrorIs(t, e, service.ErrClashNotFound)
	_, e = integrationDB.Exec(`UPDATE proxies SET assigned_supplier_user_id=$1 WHERE id=$2`, a.ID, ids[1])
	require.NoError(t, e)
	grant, e = svc.Reset(ctx, actor, 2, 90)
	require.NoError(t, e)
	parsed, e = url.Parse(grant.SubscriptionURL)
	require.NoError(t, e)
	token = parsed.Query().Get("token")
	_, e = integrationDB.Exec(`UPDATE users SET deleted_at=now() WHERE id=$1`, a.ID)
	require.NoError(t, e)
	_, _, e = svc.Snapshot(ctx, token)
	require.ErrorIs(t, e, service.ErrClashNotFound)
	_, e = svc.GetMetadata(ctx, actor)
	require.ErrorIs(t, e, service.ErrClashForbidden)
}

func TestClashSubscriptionDatabaseConstraints(t *testing.T) {
	ctx := context.Background()
	u := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleAdmin})
	other := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleAdmin})
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM users WHERE id IN ($1,$2)`, u.ID, other.ID) })
	// A role/status update with no subscription must remain harmless for old binaries.
	_, err := integrationDB.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, other.ID)
	require.NoError(t, err)
	repo := NewClashSubscriptionRepository(integrationDB)
	actor := service.SubscriptionActor{UserID: u.ID, Role: u.Role}
	create := func() error {
		return repo.WithLockedOwner(ctx, actor, func(tx service.ClashSubscriptionTx) error {
			return tx.Create(ctx, &service.ProxySubscription{TokenHash: u.Email, ExpiresAt: time.Now().Add(time.Hour)})
		})
	}
	require.NoError(t, create())
	require.ErrorIs(t, create(), service.ErrClashConflict)
	_, err = integrationDB.Exec(`INSERT INTO proxy_subscriptions(user_id,owner_role,token_hash,expires_at) VALUES($1,'admin',$2,now())`, other.ID, u.Email)
	require.Error(t, err)
	_, err = integrationDB.Exec(`UPDATE proxy_subscriptions SET owner_role='user' WHERE user_id=$1`, u.ID)
	require.Error(t, err)
	_, err = integrationDB.Exec(`UPDATE proxy_subscriptions SET version=0 WHERE user_id=$1`, u.ID)
	require.Error(t, err)
	_, err = integrationDB.Exec(`UPDATE proxy_subscriptions SET expires_at=now()-interval '1 minute' WHERE user_id=$1`, u.ID)
	require.NoError(t, err)
	require.ErrorIs(t, repo.ReadDownloadSnapshot(ctx, u.Email, func(*service.ProxySubscription, []service.Proxy) error {
		t.Fatal("expired subscription exported")
		return nil
	}), service.ErrClashNotFound)
}

func TestClashSubscriptionInFlightSnapshotBoundary(t *testing.T) {
	ctx := context.Background()
	u := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleSupplier})
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM users WHERE id=$1`, u.ID) })
	repo := NewClashSubscriptionRepository(integrationDB)
	actor := service.SubscriptionActor{UserID: u.ID, Role: u.Role}
	require.NoError(t, repo.WithLockedOwner(ctx, actor, func(tx service.ClashSubscriptionTx) error {
		return tx.Create(ctx, &service.ProxySubscription{TokenHash: u.Email, ExpiresAt: time.Now().Add(time.Hour)})
	}))
	visited := false
	require.NoError(t, repo.ReadDownloadSnapshot(ctx, u.Email, func(s *service.ProxySubscription, p []service.Proxy) error {
		// Commit revocation on a second connection while this snapshot is in flight.
		require.NoError(t, repo.WithLockedOwner(ctx, actor, func(tx service.ClashSubscriptionTx) error { return tx.Revoke(ctx, 1) }))
		require.Nil(t, s.RevokedAt)
		visited = true
		return nil
	}))
	require.True(t, visited)
	require.ErrorIs(t, repo.ReadDownloadSnapshot(ctx, u.Email, func(*service.ProxySubscription, []service.Proxy) error {
		t.Fatal("new download accepted revoked token")
		return nil
	}), service.ErrClashNotFound)
}

func TestClashSubscriptionOwnerSerialization(t *testing.T) {
	for _, disableFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "reset-before-disable", true: "disable-before-reset"}[disableFirst], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			u := mustCreateUser(t, testEntClient(t), &service.User{Role: service.RoleSupplier})
			t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM users WHERE id=$1`, u.ID) })
			repo := NewClashSubscriptionRepository(integrationDB)
			actor := service.SubscriptionActor{UserID: u.ID, Role: u.Role}
			err := repo.WithLockedOwner(ctx, actor, func(tx service.ClashSubscriptionTx) error {
				return tx.Create(ctx, &service.ProxySubscription{TokenHash: u.Email, ExpiresAt: time.Now().Add(time.Hour)})
			})
			require.NoError(t, err)
			first, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = first.Rollback() }()
			var blockerPID int
			require.NoError(t, first.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
			_, err = first.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, u.ID)
			require.NoError(t, err)
			if disableFirst {
				_, err = first.ExecContext(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, u.ID)
			} else {
				_, err = first.ExecContext(ctx, `UPDATE proxy_subscriptions SET token_hash=token_hash||'-reset',version=version+1 WHERE user_id=$1`, u.ID)
			}
			require.NoError(t, err)
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				close(started)
				if disableFirst {
					done <- repo.WithLockedOwner(ctx, actor, func(tx service.ClashSubscriptionTx) error {
						return tx.Reset(ctx, &service.ProxySubscription{TokenHash: u.Email + "-late", ExpiresAt: time.Now().Add(time.Hour)}, 1)
					})
				} else {
					_, e := integrationDB.ExecContext(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, u.ID)
					done <- e
				}
			}()
			<-started
			require.Eventually(t, func() bool {
				var blocked bool
				err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&blocked)
				return err == nil && blocked
			}, 5*time.Second, 10*time.Millisecond, "second transaction must be observed waiting on the first before commit")
			require.NoError(t, first.Commit())
			if disableFirst {
				require.ErrorIs(t, <-done, service.ErrClashForbidden)
			} else {
				require.NoError(t, <-done)
			}
			_, err = integrationDB.ExecContext(ctx, `UPDATE users SET status='active' WHERE id=$1`, u.ID)
			require.NoError(t, err)
			for _, hash := range []string{u.Email, u.Email + "-reset", u.Email + "-late"} {
				require.ErrorIs(t, repo.ReadDownloadSnapshot(ctx, hash, func(*service.ProxySubscription, []service.Proxy) error {
					t.Fatal("revoked credential restored")
					return nil
				}), service.ErrClashNotFound)
			}
		})
	}
}
