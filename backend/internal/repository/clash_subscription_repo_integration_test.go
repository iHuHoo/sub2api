//go:build integration

package repository

import (
	"context"
	"database/sql"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

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
