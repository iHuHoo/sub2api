//go:build integration

package repository

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAssignedProxySupplierLifecycleIntegration(t *testing.T) {
	ctx := context.Background()
	c := testEntClient(t)
	var userID, proxyID, accountID, groupID int64
	t.Cleanup(func() {
		for _, q := range []struct {
			sql string
			id  int64
		}{
			{"DELETE FROM account_groups WHERE account_id=$1", accountID},
			{"DELETE FROM scheduler_outbox WHERE account_id=$1", accountID},
			{"DELETE FROM accounts WHERE id=$1", accountID},
			{"DELETE FROM proxies WHERE id=$1", proxyID},
			{"DELETE FROM groups WHERE id=$1", groupID},
			{"DELETE FROM auth_identities WHERE user_id=$1", userID},
			{"DELETE FROM users WHERE id=$1", userID},
		} {
			if q.id != 0 {
				_, e := c.ExecContext(ctx, q.sql, q.id)
				if e != nil {
					t.Errorf("fixture cleanup: %v", e)
				}
			}
		}
	})
	ar := newAccountRepositoryWithSQL(c, c, nil)
	ur := newUserRepositoryWithSQL(c, c)
	u, err := c.User.Create().SetEmail(fmt.Sprintf("lifecycle-%d@test.invalid", time.Now().UnixNano())).SetPasswordHash("unused").SetRole("supplier").SetStatus("active").Save(ctx)
	require.NoError(t, err)
	userID = u.ID
	pr := newProxyRepositoryWithSQL(c, c)
	p := &service.Proxy{Name: "lifecycle", Protocol: "http", Host: "private.internal", Port: 80, Status: service.StatusActive}
	require.NoError(t, pr.Create(ctx, p))
	proxyID = p.ID
	_, err = pr.AssignSupplier(ctx, p.ID, nil, &u.ID)
	require.NoError(t, err)
	a, err := c.Account.Create().SetName("lifecycle").SetPlatform("openai").SetType("apikey").SetCredentials(map[string]any{}).SetSupplierUserID(u.ID).SetProxyID(p.ID).SetSchedulable(true).Save(ctx)
	require.NoError(t, err)
	accountID = a.ID
	g, err := c.Group.Create().SetName(fmt.Sprintf("lifecycle-%d", time.Now().UnixNano())).SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	groupID = g.ID
	_, err = c.AccountGroup.Create().SetAccountID(a.ID).SetGroupID(g.ID).Save(ctx)
	require.NoError(t, err)
	countEvents := func(accountID int64) (int, error) {
		rows, e := c.QueryContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE ($1::bigint = 0 OR account_id=$1)", accountID)
		if e != nil {
			return 0, e
		}
		defer rows.Close()
		n := 0
		if rows.Next() {
			e = rows.Scan(&n)
		}
		return n, e
	}
	check := func(want bool) {
		t.Helper()
		fresh, e := ar.GetByID(ctx, a.ID)
		require.NoError(t, e)
		require.Equal(t, want, fresh.IsSchedulable())
		require.Equal(t, want, fresh.IsCredentialUsableForShadow())
		loads, e := ar.ListSchedulableAccountLoads(ctx)
		require.NoError(t, e)
		found := false
		for _, l := range loads {
			if l.ID == a.ID {
				found = true
			}
		}
		require.Equal(t, want, found)
		rows, e := ar.ListSchedulableCapacityByGroupIDs(ctx, []int64{g.ID})
		require.NoError(t, e)
		require.Equal(t, want, len(rows) > 0)
		visible, total, e := newSupplierRepository(c, c, nil).ListProxies(ctx, u.ID, pagination.PaginationParams{Page: 1, PageSize: 10})
		require.NoError(t, e)
		if want {
			require.EqualValues(t, 1, total)
			require.Len(t, visible, 1)
		} else {
			require.Zero(t, total)
			require.Empty(t, visible)
		}
		live, e := pr.GetByID(ctx, p.ID)
		require.NoError(t, e)
		require.Equal(t, &u.ID, live.AssignedSupplierUserID)
	}
	update := func(role, status string) {
		t.Helper()
		user, e := ur.GetByID(ctx, u.ID)
		require.NoError(t, e)
		user.Role = role
		user.Status = status
		before, e := countEvents(a.ID)
		require.NoError(t, e)
		require.NoError(t, ur.Update(ctx, user, service.UserUpdateFields{Role: true, Status: true}))
		after, e := countEvents(a.ID)
		require.NoError(t, e)
		require.Greater(t, after, before)
	}
	check(true)
	// Proxy changes must refresh accounts without billing probe metadata, including expiry-only edits.
	for _, expire := range []bool{true, false} {
		live, e := pr.GetByID(ctx, p.ID)
		require.NoError(t, e)
		if expire {
			past := time.Now().Add(-time.Hour)
			live.ExpiresAt = &past
		} else {
			live.ExpiresAt = nil
		}
		before, e := countEvents(a.ID)
		require.NoError(t, e)
		require.NoError(t, pr.Update(ctx, live))
		after, e := countEvents(a.ID)
		require.NoError(t, e)
		require.Greater(t, after, before)
		fresh, e := ar.GetByID(ctx, a.ID)
		require.NoError(t, e)
		require.Equal(t, !expire, fresh.IsSchedulable())
	}
	beforeProxyRollback, e := countEvents(a.ID)
	require.NoError(t, e)
	proxyTx, e := c.Tx(ctx)
	require.NoError(t, e)
	proxyClient := proxyTx.Client()
	proxyRepo := newProxyRepositoryWithSQL(proxyClient, proxyClient)
	live, e := proxyRepo.GetByID(ctx, p.ID)
	require.NoError(t, e)
	live.Status = service.StatusDisabled
	require.NoError(t, proxyRepo.Update(ctx, live))
	require.NoError(t, proxyTx.Rollback())
	afterProxyRollback, e := countEvents(a.ID)
	require.NoError(t, e)
	require.Equal(t, beforeProxyRollback, afterProxyRollback)
	check(true)
	update("supplier", "disabled")
	check(false)
	update("supplier", "active")
	check(true)
	update("user", "active")
	check(false)
	update("supplier", "active")
	check(true)
	before, err := countEvents(a.ID)
	require.NoError(t, err)
	rollbackTx, err := c.Tx(ctx)
	require.NoError(t, err)
	rollbackClient := rollbackTx.Client()
	rollbackUsers := newUserRepositoryWithSQL(rollbackClient, rollbackClient)
	user, err := rollbackUsers.GetByID(ctx, u.ID)
	require.NoError(t, err)
	user.Status = "disabled"
	require.NoError(t, rollbackUsers.Update(ctx, user, service.UserUpdateFields{Status: true}))
	inside, err := newAccountRepositoryWithSQL(rollbackClient, rollbackClient, nil).GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, inside.IsSchedulable())
	require.NoError(t, rollbackTx.Rollback())
	after, err := countEvents(a.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	check(true)
	deleteBefore, err := countEvents(a.ID)
	require.NoError(t, err)
	require.NoError(t, ur.Delete(ctx, u.ID))
	deleteAfter, err := countEvents(a.ID)
	require.NoError(t, err)
	require.Greater(t, deleteAfter, deleteBefore)
	check(false)
	_, err = c.ExecContext(ctx, "UPDATE users SET deleted_at=NULL WHERE id=$1", u.ID)
	require.NoError(t, err)
	check(true)
	_, err = c.Account.UpdateOneID(a.ID).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	update("supplier", "disabled")
	update("supplier", "active")
	fresh, err := ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, fresh.Schedulable)
	require.False(t, fresh.IsSchedulable())
	_, err = c.Account.UpdateOneID(a.ID).SetSchedulable(true).SetSupplierPaused(true).Save(ctx)
	require.NoError(t, err)
	update("supplier", "disabled")
	update("supplier", "active")
	fresh, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, fresh.SupplierPaused)
	require.False(t, fresh.IsSchedulable())
}
