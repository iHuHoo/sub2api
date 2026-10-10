//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (s *UserRepoSuite) TestSupplierAuthVersionAtomicRevocation() {
	user := s.mustCreateUser(&service.User{Email: "supplier-generation@test.com", Role: service.RoleAdmin})
	require.Zero(s.T(), user.AuthVersion)
	for i, role := range []string{service.RoleSupplier, service.RoleAdmin} {
		user.Role = role
		require.NoError(s.T(), s.repo.Update(s.ctx, user, service.UserUpdateFields{Role: true}))
		require.Equal(s.T(), int64(i+1), user.AuthVersion)
	}
	for i, status := range []string{service.StatusDisabled, service.StatusActive} {
		user.Status = status
		require.NoError(s.T(), s.repo.Update(s.ctx, user, service.UserUpdateFields{Status: true}))
		require.Equal(s.T(), int64(i+3), user.AuthVersion)
	}
	require.NoError(s.T(), s.repo.Update(s.ctx, user, service.UserUpdateFields{Role: true, Status: true}))
	require.Equal(s.T(), int64(4), user.AuthVersion, "same role/status does not revoke sessions")
	user.AuthVersion = 0
	user.Username = "renamed"
	require.NoError(s.T(), s.repo.Update(s.ctx, user, service.UserUpdateFields{Username: true}))
	require.Equal(s.T(), int64(4), user.AuthVersion, "stale generation cannot overwrite persisted generation")
	names, err := s.repo.GetSupplierNames(s.ctx, []int64{user.ID})
	require.NoError(s.T(), err)
	require.Equal(s.T(), "renamed", names[user.ID], "provenance survives role changes")
	now := time.Now()
	_, err = s.client.User.UpdateOneID(user.ID).SetDeletedAt(now).Save(s.ctx)
	require.NoError(s.T(), err)
	names, err = s.repo.GetSupplierNames(s.ctx, []int64{user.ID})
	require.NoError(s.T(), err)
	require.Equal(s.T(), "renamed", names[user.ID], "provenance survives soft deletion")
}

func TestSupplierOwnershipPausePersistenceAndCandidates(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	proxyRepo := newProxyRepositoryWithSQL(client, tx)
	owner := int64(7)
	other := int64(8)
	note := "supplier private note"
	parent := &service.Account{Name: "supplier-parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, SupplierUserID: &owner, SupplierNotes: &note}
	require.NoError(t, repo.Create(ctx, parent))
	shadow := &service.Account{Name: "supplier-shadow", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, ParentAccountID: &parent.ID, QuotaDimension: service.QuotaDimensionSpark, SupplierUserID: &owner}
	require.NoError(t, repo.Create(ctx, shadow))
	group := mustCreateGroup(t, client, &service.Group{Name: "supplier-candidates", Platform: service.PlatformOpenAI})
	require.NoError(t, repo.BindGroups(ctx, parent.ID, []int64{group.ID}))
	require.NoError(t, repo.BindGroups(ctx, shadow.ID, []int64{group.ID}))
	require.False(t, parent.SupplierPaused, "migration/create default is unpaused")
	// A supplier pause may land after an administrator loads the account.
	stale, err := repo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	_, err = client.Account.UpdateOneID(parent.ID).SetSupplierPaused(true).Save(ctx)
	require.NoError(t, err)
	stale.SupplierUserID = &other
	stale.Name = "admin-renamed"
	require.NoError(t, repo.Update(ctx, stale))
	got, err := repo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, &owner, got.SupplierUserID, "normal update never transfers ownership")
	require.True(t, got.SupplierPaused, "stale admin edit must preserve supplier pause")
	require.Equal(t, &note, got.SupplierNotes)
	calls := []func() ([]service.Account, error){
		func() ([]service.Account, error) { return repo.ListSchedulable(ctx) },
		func() ([]service.Account, error) { return repo.ListSchedulableByPlatform(ctx, service.PlatformOpenAI) },
		func() ([]service.Account, error) {
			return repo.ListSchedulableByPlatforms(ctx, []string{service.PlatformOpenAI})
		},
		func() ([]service.Account, error) { return repo.ListSchedulableByGroupID(ctx, group.ID) },
		func() ([]service.Account, error) {
			return repo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, service.PlatformOpenAI)
		},
		func() ([]service.Account, error) {
			return repo.ListSchedulableByGroupIDAndPlatforms(ctx, group.ID, []string{service.PlatformOpenAI})
		},
		func() ([]service.Account, error) {
			return repo.ListSchedulableUngroupedByPlatform(ctx, service.PlatformOpenAI)
		},
		func() ([]service.Account, error) {
			return repo.ListSchedulableUngroupedByPlatforms(ctx, []string{service.PlatformOpenAI})
		},
		func() ([]service.Account, error) {
			return repo.ListModelAvailabilityCandidates(ctx, &group.ID, []string{service.PlatformOpenAI}, false)
		},
		func() ([]service.Account, error) {
			return repo.ListModelAvailabilityCandidates(ctx, nil, []string{service.PlatformOpenAI}, true)
		},
	}
	for i, call := range calls {
		candidates, err := call()
		require.NoError(t, err, "candidate query %d", i)
		for _, candidate := range candidates {
			require.NotEqual(t, parent.ID, candidate.ID)
			require.NotEqual(t, shadow.ID, candidate.ID)
		}
	}
	pausedAccounts, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, service.PlatformOpenAI, "", "unschedulable", "", 0, "")
	require.NoError(t, err)
	pausedIDs := make([]int64, 0, len(pausedAccounts))
	for _, paused := range pausedAccounts {
		pausedIDs = append(pausedIDs, paused.ID)
	}
	require.Contains(t, pausedIDs, parent.ID, "supplier pause appears in administrator unschedulable filter")
	require.Contains(t, pausedIDs, shadow.ID, "parent pause appears in shadow filter")
	loads, err := repo.ListSchedulableAccountLoads(ctx)
	require.NoError(t, err)
	for _, load := range loads {
		require.NotEqual(t, parent.ID, load.ID)
		require.NotEqual(t, shadow.ID, load.ID)
	}
	capacity, err := repo.ListSchedulableCapacityByGroupIDs(ctx, []int64{group.ID})
	require.NoError(t, err)
	require.Empty(t, capacity)
	_, err = client.Account.UpdateOneID(parent.ID).SetSupplierPaused(false).SetStatus(service.StatusDisabled).Save(ctx)
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.False(t, got.IsSchedulable(), "supplier resume cannot undo administrator disable")
	proxy := &service.Proxy{Name: "supplier-proxy", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive, SupplierUserID: &owner}
	require.NoError(t, proxyRepo.Create(ctx, proxy))
	proxy.SupplierUserID = &other
	proxy.Name = "admin-proxy-renamed"
	require.NoError(t, proxyRepo.Update(ctx, proxy))
	storedProxy, err := proxyRepo.GetByID(ctx, proxy.ID)
	require.NoError(t, err)
	require.Equal(t, &owner, storedProxy.SupplierUserID)
	require.NoError(t, repo.Delete(ctx, parent.ID))
	deleted, err := client.Account.Get(mixins.SkipSoftDelete(ctx), parent.ID)
	require.NoError(t, err)
	require.Equal(t, &owner, deleted.SupplierUserID, "soft deletion retains provenance")
	platform := &service.Account{Name: "platform", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true}
	require.NoError(t, repo.Create(ctx, platform))
	platformStored, err := repo.GetByID(ctx, platform.ID)
	require.NoError(t, err)
	require.Nil(t, platformStored.SupplierUserID)
	require.False(t, platformStored.SupplierPaused)
}

func TestSupplierSchedulerCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	cache := NewSchedulerCache(testRedis(t))
	owner := int64(7)
	account := service.Account{ID: 98761, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, SupplierUserID: &owner, SupplierPaused: true}
	bucket := service.SchedulerBucket{Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, &owner, snapshot[0].SupplierUserID)
	require.True(t, snapshot[0].SupplierPaused)
	require.False(t, snapshot[0].IsSchedulable())
	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, &owner, full.SupplierUserID)
	require.True(t, full.SupplierPaused)
}

func TestSupplierMigrationDefaultsAndIndexes(t *testing.T) {
	tx := testTx(t)
	requireColumn(t, tx, "accounts", "supplier_user_id", "bigint", 0, true)
	requireColumn(t, tx, "accounts", "supplier_paused", "boolean", 0, false)
	requireColumnDefaultContains(t, tx, "accounts", "supplier_paused", "false")
	requireColumn(t, tx, "accounts", "supplier_notes", "text", 0, true)
	requireIndex(t, tx, "accounts", "accounts_supplier_user_id_idx")
	requireColumn(t, tx, "proxies", "supplier_user_id", "bigint", 0, true)
	requireIndex(t, tx, "proxies", "proxies_supplier_user_id_idx")
	requireColumn(t, tx, "users", "auth_version", "bigint", 0, false)
	requireColumnDefaultContains(t, tx, "users", "auth_version", "0")
}
