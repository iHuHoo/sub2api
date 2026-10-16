//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
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
		legacy := []*service.Account{{ID: a.ID, Status: service.StatusActive, Schedulable: true}}
		require.NoError(t, ar.RefreshAccountProxies(ctx, legacy))
		require.Equal(t, &p.ID, legacy[0].ProxyID)
		require.Equal(t, want, legacy[0].IsSchedulable())
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
	// Retain production payloads across committed binding and assignment changes.
	_, err = c.Account.UpdateOneID(a.ID).SetSupplierPaused(false).Save(ctx)
	require.NoError(t, err)
	fresh, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	full, meta, err := marshalSchedulerCacheAccount(*fresh)
	require.NoError(t, err)
	verifyPayload := func(payload []byte, bound bool, usable bool) {
		t.Helper()
		var cached service.Account
		require.NoError(t, json.Unmarshal(payload, &cached))
		snapshot := service.NewSchedulerSnapshotService(&assignmentBoundaryCache{snapshot: []*service.Account{&cached}}, nil, ar, nil, nil)
		got, e := snapshot.GetAccount(ctx, a.ID)
		require.NoError(t, e)
		require.Equal(t, usable, got.IsSchedulable())
		if bound {
			require.Equal(t, &p.ID, got.ProxyID)
		} else {
			require.Nil(t, got.ProxyID)
			require.Nil(t, got.Proxy)
		}
	}
	_, err = c.Account.UpdateOneID(a.ID).ClearProxyID().Save(ctx)
	require.NoError(t, err)
	_, err = pr.AssignSupplier(ctx, p.ID, &u.ID, nil)
	require.NoError(t, err)
	for _, payload := range [][]byte{full, meta} {
		verifyPayload(payload, false, true)
	}
	other, err := c.User.Create().SetEmail(fmt.Sprintf("binding-other-%d@test.invalid", time.Now().UnixNano())).SetPasswordHash("unused").SetRole("supplier").SetStatus("active").Save(ctx)
	require.NoError(t, err)
	defer func() { _, e := c.ExecContext(ctx, "DELETE FROM users WHERE id=$1", other.ID); require.NoError(t, e) }()
	_, err = pr.AssignSupplier(ctx, p.ID, nil, &other.ID)
	require.NoError(t, err)
	for _, payload := range [][]byte{full, meta} {
		verifyPayload(payload, false, true)
	}
	_, err = pr.AssignSupplier(ctx, p.ID, &other.ID, &u.ID)
	require.NoError(t, err)
	fresh, err = ar.GetByID(ctx, a.ID)
	require.NoError(t, err)
	full, meta, err = marshalSchedulerCacheAccount(*fresh)
	require.NoError(t, err)
	_, err = c.Account.UpdateOneID(a.ID).SetProxyID(p.ID).Save(ctx)
	require.NoError(t, err)
	_, err = c.Proxy.UpdateOneID(p.ID).SetStatus("disabled").Save(ctx)
	require.NoError(t, err)
	for _, payload := range [][]byte{full, meta} {
		verifyPayload(payload, true, false)
	}

}
