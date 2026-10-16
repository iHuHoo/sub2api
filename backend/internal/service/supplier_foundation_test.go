//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSupplierPauseNeverOverridesAdminStatus(t *testing.T) {
	a := &Account{Status: StatusActive, Schedulable: true, SupplierPaused: true}
	require.False(t, a.IsSchedulable())
	require.False(t, a.IsCredentialUsableForShadow())
	a.SupplierPaused = false
	a.Status = StatusDisabled
	require.False(t, a.IsSchedulable())
	require.False(t, a.IsCredentialUsableForShadow())
}

func TestSupplierRoleNormalization(t *testing.T) {
	role, err := normalizeUserRole("supplier", RoleUser)
	require.NoError(t, err)
	require.Equal(t, "supplier", role)
	repo := &userRepoStub{nextID: 33}
	svc := &adminServiceImpl{userRepo: repo}
	user, err := svc.CreateUser(context.Background(), &CreateUserInput{Email: "supplier@test.com", Password: "strong-pass", Role: "supplier"})
	require.NoError(t, err)
	require.Equal(t, "supplier", user.Role)
}

func TestSupplierRoleCannotDemoteLastAdmin(t *testing.T) {
	repo := &roleGuardUserRepoStub{rpmUserRepoStub: &rpmUserRepoStub{userRepoStub: &userRepoStub{user: &User{ID: 42, Role: RoleAdmin}}}, adminTotal: 1}
	svc := &adminServiceImpl{userRepo: repo}
	_, err := svc.UpdateUser(context.Background(), 42, &UpdateUserInput{Role: "supplier"})
	require.ErrorContains(t, err, "last admin")
	require.Nil(t, repo.lastUpdated)
}

func TestSupplierAuthVersionRevokesRestoredRoleAndStatus(t *testing.T) {
	user := &User{ID: 42, Email: "admin@test.com", PasswordHash: "hash", Role: RoleAdmin, Status: StatusActive}
	original := resolvedTokenVersion(user)
	user.Role = "supplier"
	user.AuthVersion++
	require.NotEqual(t, original, resolvedTokenVersion(user))
	user.Role = RoleAdmin
	user.AuthVersion++
	require.NotEqual(t, original, resolvedTokenVersion(user))
	user.Status = StatusDisabled
	user.AuthVersion++
	user.Status = StatusActive
	user.AuthVersion++
	require.NotEqual(t, original, resolvedTokenVersion(user))
}

func TestSupplierShadowInheritsOwnerAndPause(t *testing.T) {
	repo := newSparkShadowRepoStub()
	owner := int64(7)
	parent := &Account{Name: "p", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, SupplierUserID: &owner, SupplierPaused: true, Credentials: map[string]any{"refresh_token": "RT", "chatgpt_account_id": "org"}}
	require.NoError(t, repo.Create(context.Background(), parent))
	shadow, err := (&adminServiceImpl{accountRepo: repo}).CreateShadow(context.Background(), parent.ID, ShadowOptions{})
	require.NoError(t, err)
	require.Equal(t, parent.SupplierUserID, shadow.SupplierUserID)
	require.False(t, shadow.SupplierPaused)
	require.False(t, parentHealthyForShadow(shadow, func(int64) *Account { return parent }))
	parent.SupplierPaused = false
	require.True(t, parentHealthyForShadow(shadow, func(int64) *Account { return parent }))
}

type supplierNameRepoStub struct {
	*userRepoStub
	calls int
	ids   []int64
}

func (r *supplierNameRepoStub) GetSupplierNames(_ context.Context, ids []int64) (map[int64]string, error) {
	r.calls++
	r.ids = ids
	return map[int64]string{7: "Supplier"}, nil
}
func TestSupplierNamesLoadedInBatchWithIDFallback(t *testing.T) {
	owner, missing := int64(7), int64(8)
	accounts := []Account{{SupplierUserID: &owner}, {SupplierUserID: &owner}, {SupplierUserID: &missing}, {}}
	repo := &supplierNameRepoStub{userRepoStub: &userRepoStub{}}
	svc := &adminServiceImpl{userRepo: repo}
	require.NoError(t, svc.loadSupplierNames(context.Background(), accounts))
	require.Equal(t, 1, repo.calls)
	require.ElementsMatch(t, []int64{7, 8}, repo.ids)
	require.Equal(t, "Supplier", accounts[0].SupplierName)
	require.Equal(t, "Supplier", accounts[1].SupplierName)
	require.Equal(t, "#8", accounts[2].SupplierName)
	require.Empty(t, accounts[3].SupplierName)
}

func TestSupplierPauseFiltersCachedSnapshot(t *testing.T) {
	owner := int64(7)
	cache := &snapshotHydrationCache{snapshot: []*Account{{ID: 1, SupplierUserID: &owner, SupplierPaused: true}, {ID: 2, SupplierUserID: &owner}}}
	snapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	candidates, _, err := snapshot.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, true)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, int64(2), candidates[0].ID)
	require.Equal(t, &owner, candidates[0].SupplierUserID)
}

type supplierRefreshCache struct {
	refreshTokenCacheStub
	data map[string]*RefreshTokenData
}

func (c *supplierRefreshCache) StoreRefreshToken(_ context.Context, hash string, data *RefreshTokenData, _ time.Duration) error {
	c.data[hash] = data
	return nil
}
func (c *supplierRefreshCache) GetRefreshToken(_ context.Context, hash string) (*RefreshTokenData, error) {
	return c.data[hash], nil
}

func TestSupplierPrivilegeChangesRevokeJWTAndRefresh(t *testing.T) {
	for _, change := range []string{"role", "status"} {
		t.Run(change, func(t *testing.T) {
			user := &User{ID: 42, Email: "admin@test.com", PasswordHash: "hash", Role: RoleAdmin, Status: StatusActive}
			repo := &userRepoStub{user: user}
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-supplier-secret", AccessTokenExpireMinutes: 60, RefreshTokenExpireDays: 7}}
			cache := &supplierRefreshCache{data: make(map[string]*RefreshTokenData)}
			svc := &AuthService{userRepo: repo, cfg: cfg, refreshTokenCache: cache}
			pair, err := svc.GenerateTokenPair(context.Background(), user, "")
			require.NoError(t, err)
			if change == "role" {
				user.Role = RoleSupplier
			} else {
				user.Status = StatusDisabled
			}
			user.AuthVersion = 1
			_, err = svc.RefreshToken(context.Background(), pair.AccessToken)
			require.Error(t, err)
			_, err = svc.RefreshTokenPair(context.Background(), pair.RefreshToken)
			require.Error(t, err)
			user.Role = RoleAdmin
			user.Status = StatusActive
			user.AuthVersion = 2
			_, err = svc.RefreshToken(context.Background(), pair.AccessToken)
			require.ErrorIs(t, err, ErrTokenRevoked)
			_, err = svc.RefreshTokenPair(context.Background(), pair.RefreshToken)
			require.ErrorIs(t, err, ErrTokenRevoked)
		})
	}
}

func TestSupplierPauseAfterSnapshotBlocksHydratedSelection(t *testing.T) {
	cache := &snapshotHydrationCache{accounts: map[int64]*Account{1: {ID: 1, SupplierPaused: true}}}
	snapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	candidate := &Account{ID: 1}
	releases := 0
	openai := &OpenAIGatewayService{schedulerSnapshot: snapshot}
	selection, err := openai.newAcquiredSelectionResult(context.Background(), candidate, func() { releases++ })
	require.Error(t, err)
	require.Nil(t, selection)
	require.Equal(t, 1, releases)
	gateway := &GatewayService{schedulerSnapshot: snapshot}
	selection, err = gateway.newSelectionResult(context.Background(), candidate, true, func() { releases++ }, nil)
	require.Error(t, err)
	require.Nil(t, selection)
	require.Equal(t, 2, releases)
}
