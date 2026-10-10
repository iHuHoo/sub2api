//go:build integration

package repository

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSupplierRepositoryIsolationAndSparseMutation(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newSupplierRepository(client, tx, nil)
	accounts := newAccountRepositoryWithSQL(client, tx, nil)
	proxies := newProxyRepositoryWithSQL(client, tx)
	owner := int64(11)
	other := int64(22)
	makeAccount := func(o *int64) *service.Account {
		a := &service.Account{Name: "account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "old", "model_mapping": map[string]any{"a": "b"}}, Status: service.StatusDisabled, Schedulable: false, Priority: 99, SupplierUserID: o}
		require.NoError(t, accounts.Create(ctx, a))
		return a
	}
	owned := makeAccount(&owner)
	foreign := makeAccount(&other)
	platform := makeAccount(nil)
	list, total, err := repo.ListAccounts(ctx, owner, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, list, 1)
	for _, id := range []int64{foreign.ID, platform.ID, 999999} {
		_, err = repo.GetAccount(ctx, owner, id)
		require.ErrorIs(t, err, service.ErrSupplierUnavailable)
		_, err = repo.UpdateAccount(ctx, owner, id, service.SupplierAccountUpdate{Name: supplierPtr("stolen")})
		require.ErrorIs(t, err, service.ErrSupplierUnavailable)
		_, err = repo.PauseAccount(ctx, owner, id, true)
		require.ErrorIs(t, err, service.ErrSupplierUnavailable)
	}
	got, err := repo.PauseAccount(ctx, owner, owned.ID, true)
	require.NoError(t, err)
	require.True(t, got.SupplierPaused)
	require.Equal(t, service.StatusDisabled, got.Status)
	require.False(t, got.Schedulable)
	got, err = repo.UpdateAccount(ctx, owner, owned.ID, service.SupplierAccountUpdate{Name: supplierPtr("renamed"), Notes: supplierPtr("supplier-note"), Credentials: map[string]any{"api_key": "new"}})
	require.NoError(t, err)
	require.Equal(t, 99, got.Priority)
	require.True(t, got.SupplierPaused)
	require.Equal(t, "new", got.Credentials["api_key"])
	require.Contains(t, got.Credentials, "model_mapping")
	require.Nil(t, got.Notes)
	require.Equal(t, "supplier-note", *got.SupplierNotes)
	p := &service.Proxy{Name: "p", Protocol: "http", Host: "8.8.8.8", Port: 8080, Status: service.StatusActive, SupplierUserID: &other}
	require.NoError(t, proxies.Create(ctx, p))
	_, err = repo.UpdateAccount(ctx, owner, owned.ID, service.SupplierAccountUpdate{ProxyID: &p.ID})
	require.ErrorIs(t, err, service.ErrSupplierUnavailable)
	_, err = client.Account.UpdateOneID(owned.ID).SetProxyID(p.ID).Save(ctx)
	require.NoError(t, err)
	restricted, err := repo.GetAccount(ctx, owner, owned.ID)
	require.NoError(t, err)
	require.Nil(t, restricted.Proxy, "foreign proxy is not hydrated into supplier repository results")
	p.SupplierUserID = &owner
	p.ID = 0
	require.NoError(t, proxies.Create(ctx, p))
	_, err = repo.UpdateAccount(ctx, owner, owned.ID, service.SupplierAccountUpdate{ProxyID: &p.ID})
	require.NoError(t, err)
	require.ErrorIs(t, repo.DeleteProxy(ctx, owner, p.ID), service.ErrSupplierProxyInUse)
	_, err = client.Account.UpdateOneID(owned.ID).ClearProxyID().Save(ctx)
	require.NoError(t, err)
	_, err = client.Account.UpdateOneID(foreign.ID).SetProxyFallbackOriginID(p.ID).Save(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, repo.DeleteProxy(ctx, owner, p.ID), service.ErrSupplierProxyInUse)
	_, err = client.Account.UpdateOneID(foreign.ID).ClearProxyFallbackOriginID().Save(ctx)
	require.NoError(t, err)
	_, err = client.Proxy.Create().SetName("foreign-backup").SetProtocol("http").SetHost("8.8.4.4").SetPort(80).SetBackupProxyID(p.ID).Save(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, repo.DeleteProxy(ctx, owner, p.ID), service.ErrSupplierProxyInUse)
	rows, err := tx.QueryContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 OR payload @> jsonb_build_object('account_ids',jsonb_build_array($1::bigint))", owned.ID)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var count int
	require.NoError(t, rows.Scan(&count))
	require.GreaterOrEqual(t, count, 3)
}

func supplierPtr[T any](v T) *T { return &v }

func TestSupplierUsageRetainsDeletedOwnedHistory(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newSupplierRepository(client, tx, nil)
	ar := newAccountRepositoryWithSQL(client, tx, nil)
	owner := int64(11)
	other := int64(22)
	makeA := func(o *int64, parent *int64) *service.Account {
		a := &service.Account{Name: "a", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, SupplierUserID: o, ParentAccountID: parent}
		if parent != nil {
			a.QuotaDimension = service.QuotaDimensionSpark
		}
		require.NoError(t, ar.Create(ctx, a))
		return a
	}
	a := makeA(&owner, nil)
	shadow := makeA(&owner, &a.ID)
	foreign := makeA(&other, nil)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	// Usage rows require a real downstream user/key, neither of which is selected by aggregation.
	user := mustCreateUser(t, client, &service.User{Email: "supplier-usage@test.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-supplier-usage", Name: "key"})
	for _, id := range []int64{a.ID, shadow.ID, foreign.ID} {
		_, err := client.UsageLog.Create().SetUserID(user.ID).SetAPIKeyID(key.ID).SetAccountID(id).SetRequestID(fmt.Sprintf("supplier-%d", id)).SetModel("m").SetInputTokens(10).SetOutputTokens(20).SetCacheCreationTokens(30).SetCacheReadTokens(40).SetCreatedAt(now).Save(ctx)
		require.NoError(t, err)
	}
	_, err := client.Account.UpdateOneID(a.ID).SetDeletedAt(now).Save(ctx)
	require.NoError(t, err)
	u, err := repo.Usage(ctx, owner, now.Add(-12*time.Hour), now.Add(12*time.Hour), nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), u.Summary.Requests)
	require.Equal(t, int64(20), u.Summary.InputTokens)
	require.Len(t, u.Daily, 1)
	require.Equal(t, "2026-10-01", u.Daily[0].Date)
	u, err = repo.Usage(ctx, owner, now.Add(-12*time.Hour), now.Add(12*time.Hour), &a.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), u.Summary.Requests)
	_, err = repo.Usage(ctx, owner, now.Add(-12*time.Hour), now.Add(12*time.Hour), &foreign.ID)
	require.ErrorIs(t, err, service.ErrSupplierUnavailable)
	_, err = client.Account.Get(mixins.SkipSoftDelete(ctx), a.ID)
	require.NoError(t, err)
}

func TestSupplierServiceCreatesUngroupedAndRefreshesParentShadowCache(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	cache := NewSchedulerCache(testRedis(t))
	repo := newSupplierRepository(client, tx, cache)
	s := service.NewSupplierService(repo)
	owner := int64(11)
	other := int64(22)
	mustCreateGroup(t, client, &service.Group{Name: "openai-default", Platform: service.PlatformOpenAI})
	a, err := s.CreateAccount(ctx, owner, service.SupplierAccountCreate{Name: "supplier-import", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "write-only"}})
	require.NoError(t, err)
	stored, err := repo.GetAccount(ctx, owner, a.ID)
	require.NoError(t, err)
	require.Equal(t, &owner, stored.SupplierUserID)
	groups, err := client.AccountGroup.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, groups)
	ar := newAccountRepositoryWithSQL(client, tx, cache)
	shadow := &service.Account{Name: "shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, SupplierUserID: &owner, ParentAccountID: &a.ID, QuotaDimension: service.QuotaDimensionSpark}
	require.NoError(t, ar.Create(ctx, shadow))
	_, err = s.PauseAccount(ctx, owner, shadow.ID, true)
	require.ErrorIs(t, err, service.ErrSupplierReadOnly)
	_, err = s.UpdateAccount(ctx, owner, shadow.ID, service.SupplierAccountUpdate{Name: supplierPtr("rename")})
	require.ErrorIs(t, err, service.ErrSupplierReadOnly)
	require.NoError(t, cache.SetAccount(ctx, stored))
	require.NoError(t, cache.SetAccount(ctx, shadow))
	_, err = s.PauseAccount(ctx, owner, a.ID, true)
	require.NoError(t, err)
	parentCache, err := cache.GetAccount(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, parentCache.SupplierPaused)
	require.False(t, parentCache.IsCredentialUsableForShadow())
	shadowCache, err := cache.GetAccount(ctx, shadow.ID)
	require.NoError(t, err)
	require.Equal(t, &owner, shadowCache.SupplierUserID)
	require.Equal(t, &a.ID, shadowCache.ParentAccountID)
	p, err := s.CreateProxy(ctx, owner, service.SupplierProxyInput{Name: supplierPtr("owned"), Protocol: supplierPtr("http"), Host: supplierPtr("8.8.8.8"), Port: supplierPtr(8080), Password: supplierPtr("secret")})
	require.NoError(t, err)
	require.True(t, p.HasPassword)
	ps, total, err := repo.ListProxies(ctx, owner, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, ps, 1)
	require.Equal(t, int64(1), total)
	for _, id := range []int64{p.ID, 999999} {
		_, err = repo.GetProxy(ctx, other, id)
		require.ErrorIs(t, err, service.ErrSupplierUnavailable)
		_, err = repo.UpdateProxy(ctx, other, id, service.SupplierProxyInput{Name: supplierPtr("stolen")})
		require.ErrorIs(t, err, service.ErrSupplierUnavailable)
		require.ErrorIs(t, repo.DeleteProxy(ctx, other, id), service.ErrSupplierUnavailable)
		_, err = s.TestProxy(ctx, other, id)
		require.ErrorIs(t, err, service.ErrSupplierUnavailable)
	}
	_, err = client.Proxy.UpdateOneID(p.ID).SetFallbackMode("direct").SetExpiryWarnDays(99).Save(ctx)
	require.NoError(t, err)
	_, err = s.UpdateProxy(ctx, owner, p.ID, service.SupplierProxyInput{Name: supplierPtr("renamed")})
	require.NoError(t, err)
	pp, err := repo.GetProxy(ctx, owner, p.ID)
	require.NoError(t, err)
	require.Equal(t, "direct", pp.FallbackMode)
	require.Equal(t, 99, pp.ExpiryWarnDays)
	require.NoError(t, s.DeleteProxy(ctx, owner, p.ID))
	_, err = repo.GetProxy(ctx, owner, p.ID)
	require.ErrorIs(t, err, service.ErrSupplierUnavailable)
}

func TestSupplierCommittedTransactionsAndBoundedProxyDelete(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newSupplierRepository(client, integrationDB, nil)
	s := service.NewSupplierService(repo)
	owner := int64(990011)
	a, err := s.CreateAccount(ctx, owner, service.SupplierAccountCreate{Name: "committed", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}})
	require.NoError(t, err)
	_, err = s.PauseAccount(ctx, owner, a.ID, true)
	require.NoError(t, err)
	stored, err := repo.GetAccount(ctx, owner, a.ID)
	require.NoError(t, err)
	require.True(t, stored.SupplierPaused)
	_, err = s.UpdateAccount(ctx, owner, a.ID, service.SupplierAccountUpdate{Name: supplierPtr("must-not-write"), ProxyID: supplierPtr(int64(999999))})
	require.ErrorIs(t, err, service.ErrSupplierUnavailable)
	stored, err = repo.GetAccount(ctx, owner, a.ID)
	require.NoError(t, err)
	require.Equal(t, "committed", stored.Name, "failed sparse mutation rolled back")
	p, err := s.CreateProxy(ctx, owner, service.SupplierProxyInput{Name: supplierPtr("bounded"), Protocol: supplierPtr("http"), Host: supplierPtr("8.8.8.8"), Port: supplierPtr(80)})
	require.NoError(t, err)
	blocker := testTx(t)
	_, err = blocker.ExecContext(ctx, `LOCK TABLE accounts IN SHARE ROW EXCLUSIVE MODE`)
	require.NoError(t, err)
	start := time.Now()
	err = s.DeleteProxy(ctx, owner, p.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "lock timeout")
	require.Less(t, time.Since(start), 5*time.Second)
	_, err = repo.GetProxy(ctx, owner, p.ID)
	require.NoError(t, err, "timed-out delete retained the resource")
	require.NoError(t, blocker.Rollback())
	require.NoError(t, s.DeleteProxy(ctx, owner, p.ID))
	_, err = repo.GetProxy(ctx, owner, p.ID)
	require.ErrorIs(t, err, service.ErrSupplierUnavailable)
}
