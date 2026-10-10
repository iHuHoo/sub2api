//go:build unit

package service

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type supplierRefreshCASRepo struct{ *refreshAPIAccountRepo }

func (r *supplierRefreshCASRepo) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, id int64, observed, mapNew map[string]any) (bool, error) {
	if !reflect.DeepEqual(r.account.Credentials, observed) {
		return false, nil
	}
	return true, r.UpdateCredentials(ctx, id, mapNew)
}

func TestSupplierRotationRejectsInFlightOAuthRefresh(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			old := &Account{ID: 7, Platform: platform, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "_token_version": int64(10)}}
			repo := &supplierRefreshCASRepo{&refreshAPIAccountRepo{account: old}}
			rotated := *old
			rotated.Credentials = map[string]any{"access_token": "supplier-new", "refresh_token": "supplier-refresh", "_token_version": int64(11), "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}
			executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "stale-upstream", "refresh_token": "stale-refresh"}, onRefresh: func() { repo.account = &rotated }}
			api := NewOAuthRefreshAPI(repo, newOpenAITokenCacheStub())
			result, err := api.RefreshIfNeeded(context.Background(), old, executor, time.Minute)
			require.NoError(t, err)
			require.Equal(t, "supplier-new", repo.account.GetCredential("access_token"))
			require.Equal(t, int64(11), repo.account.GetCredentialAsInt64("_token_version"))
			require.False(t, result.Refreshed)
			require.Nil(t, result.NewCredentials)
			require.Equal(t, "supplier-new", result.Account.GetCredential("access_token"))
		})
	}
}

func TestSupplierRotationRefreshProviderPublishesCurrentToken(t *testing.T) {
	old := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "_token_version": int64(10)}}
	repo := &supplierRefreshCASRepo{&refreshAPIAccountRepo{account: old}}
	rotated := *old
	rotated.Credentials = map[string]any{"access_token": "supplier-new", "refresh_token": "supplier-refresh", "_token_version": int64(11), "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "stale-upstream", "refresh_token": "stale-refresh"}, onRefresh: func() { repo.account = &rotated }}
	cache := newOpenAITokenCacheStub()
	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)
	token, err := provider.GetAccessToken(context.Background(), old)
	require.NoError(t, err)
	require.Equal(t, "supplier-new", token)
	for _, cached := range cache.tokens {
		require.NotEqual(t, "stale-upstream", cached)
	}
}

type supplierGenerationRepo struct {
	AccountRepository
	mu sync.Mutex
	a  *Account
}

func (r *supplierGenerationRepo) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := *r.a
	a.Credentials = shallowCopyMap(r.a.Credentials)
	return &a, nil
}
func (r *supplierGenerationRepo) UpdateOAuthCredentialsIfUnchanged(_ context.Context, _ int64, observed, credentials map[string]any) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !reflect.DeepEqual(r.a.Credentials, observed) {
		return false, nil
	}
	r.a.Credentials = shallowCopyMap(credentials)
	return true, nil
}

type supplierGenerationExecutor struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (e *supplierGenerationExecutor) CanRefresh(*Account) bool { return true }
func (e *supplierGenerationExecutor) NeedsRefresh(a *Account, _ time.Duration) bool {
	expiry := a.GetCredentialAsTime("expires_at")
	return expiry == nil || expiry.Before(time.Now())
}
func (e *supplierGenerationExecutor) CacheKey(a *Account) string {
	return NewOpenAITokenRefresher(nil, nil).CacheKey(a)
}
func (e *supplierGenerationExecutor) Refresh(ctx context.Context, _ *Account) (map[string]any, error) {
	e.calls.Add(1)
	e.entered <- struct{}{}
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return map[string]any{"access_token": "refreshed", "refresh_token": "next", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}, nil
}
func TestSupplierOAuthRefreshLockSpansCredentialGenerations(t *testing.T) {
	first := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old", "refresh_token": "same", "_token_version": int64(10)}}
	latest := *first
	latest.Credentials = shallowCopyMap(first.Credentials)
	latest.Credentials["_token_version"] = int64(11)
	repo := &supplierGenerationRepo{a: &latest}
	api := NewOAuthRefreshAPI(repo, nil)
	executor := &supplierGenerationExecutor{entered: make(chan struct{}, 2), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 2)
	go func() { _, err := api.RefreshIfNeeded(ctx, first, executor, time.Minute); result <- err }()
	<-executor.entered
	go func() { _, err := api.RefreshIfNeeded(ctx, &latest, executor, time.Minute); result <- err }()
	secondEntered := false
	select {
	case <-executor.entered:
		secondEntered = true
	case <-time.After(100 * time.Millisecond):
	}
	close(executor.release)
	require.NoError(t, <-result)
	require.NoError(t, <-result)
	require.False(t, secondEntered, "same latest refresh token must not be consumed concurrently")
	require.Equal(t, int32(1), executor.calls.Load())
	locks := 0
	api.localLocks.Range(func(_, _ any) bool { locks++; return true })
	require.Equal(t, 1, locks, "rotation must not grow permanent local lock map per generation")
}
func TestSupplierRefreshKeysAreStableAcrossGenerations(t *testing.T) {
	refreshers := []OAuthRefreshExecutor{NewClaudeTokenRefresher(nil), NewOpenAITokenRefresher(nil, nil), NewGeminiTokenRefresher(nil), NewAntigravityTokenRefresher(nil), NewGrokTokenRefresher(nil)}
	for _, refresher := range refreshers {
		first := &Account{ID: 7, Credentials: map[string]any{"_token_version": int64(10)}}
		second := *first
		second.Credentials = map[string]any{"_token_version": int64(11)}
		require.Equal(t, refresher.CacheKey(first), refresher.CacheKey(&second))
	}
}

func TestSupplierProjectBackfillPreservesRotatedToken(t *testing.T) {
	for _, platform := range []string{PlatformGemini, PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			account := &Account{ID: 7, Platform: platform, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old", "_token_version": int64(10)}}
			observed := shallowCopyMap(account.Credentials)
			latest := *account
			latest.Credentials = map[string]any{"access_token": "supplier-new", "_token_version": int64(11)}
			repo := &supplierRefreshCASRepo{&refreshAPIAccountRepo{account: &latest}}
			account.Credentials["project_id"] = "network-backfill"
			applied, err := persistOAuthRefreshCredentials(context.Background(), repo, account, observed, account.Credentials)
			require.NoError(t, err)
			require.False(t, applied)
			require.Equal(t, "supplier-new", account.GetCredential("access_token"))
			require.Equal(t, int64(11), account.GetCredentialAsInt64("_token_version"))
			require.NotContains(t, repo.account.Credentials, "project_id")
		})
	}
}
func TestSupplierRefreshGenerationAdvancesFutureVersion(t *testing.T) {
	old := time.Now().Add(time.Hour).UnixMilli()
	require.Equal(t, old+1, nextOAuthTokenVersion(&Account{Credentials: map[string]any{"_token_version": old}}))
}
