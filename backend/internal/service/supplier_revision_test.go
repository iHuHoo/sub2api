//go:build unit

package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSupplierGeminiTokenIsolation(t *testing.T) {
	ctx := context.Background()
	cache := newOpenAITokenCacheStub()
	provider := NewGeminiTokenProvider(nil, cache, nil)
	first := &Account{ID: 123, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "victim", "project_id": "same", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	second := &Account{ID: 456, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "owned", "project_id": "same", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	token, err := provider.GetAccessToken(ctx, first)
	require.NoError(t, err)
	require.Equal(t, "victim", token)
	token, err = provider.GetAccessToken(ctx, second)
	require.NoError(t, err)
	require.Equal(t, "owned", token)
	require.NotEqual(t, NewGeminiTokenRefresher(nil).CacheKey(first), NewGeminiTokenRefresher(nil).CacheKey(second))
	first.Credentials["project_id"] = ""
	second.Credentials["project_id"] = "account:123"
	require.NotEqual(t, GeminiTokenCacheKey(first), GeminiTokenCacheKey(second))
	inv := NewCompositeTokenCacheInvalidator(cache)
	require.NoError(t, inv.InvalidateToken(ctx, second))
	token, err = provider.GetAccessToken(ctx, first)
	require.NoError(t, err)
	require.Equal(t, "victim", token)
}

func TestSupplierVertexTokenIsolationAndRotation(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	makeKey := func() *vertexServiceAccountKey {
		private, err := rsa.GenerateKey(rand.Reader, 1024)
		require.NoError(t, err)
		return &vertexServiceAccountKey{ProjectID: "p", ClientEmail: "same@p", PrivateKeyID: "same", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)}))}
	}
	key := makeKey()
	otherKey := makeKey()
	first := &Account{ID: 123, Platform: PlatformGemini, Type: AccountTypeServiceAccount}
	second := &Account{ID: 456, Platform: PlatformAnthropic, Type: AccountTypeServiceAccount}
	setKey := func(a *Account, k *vertexServiceAccountKey) {
		raw, err := json.Marshal(k)
		require.NoError(t, err)
		a.Credentials = map[string]any{"service_account_json": string(raw)}
	}
	setKey(first, key)
	setKey(second, otherKey)
	require.NotEqual(t, vertexServiceAccountCacheKey(first, key), vertexServiceAccountCacheKey(second, otherKey))
	old := vertexServiceAccountCacheKey(first, key)
	require.NoError(t, cache.SetAccessToken(context.Background(), old, "victim", time.Hour))
	original := http.DefaultTransport
	http.DefaultTransport = supplierRevisionTransport(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, vertexDefaultTokenURL, req.URL.String())
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"owned","expires_in":3600}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	token, err := getVertexServiceAccountAccessToken(context.Background(), cache, second)
	require.NoError(t, err)
	require.Equal(t, "owned", token)
	// Replacing the actual RSA material isolates rotation even when key metadata is unchanged.
	require.NotEqual(t, old, vertexServiceAccountCacheKey(first, otherKey))
	setKey(first, otherKey)
	token, err = getVertexServiceAccountAccessToken(context.Background(), cache, first)
	require.NoError(t, err)
	require.Equal(t, "owned", token)
	setKey(first, key)
	require.NoError(t, NewCompositeTokenCacheInvalidator(cache).InvalidateToken(context.Background(), first))
	token, err = cache.GetAccessToken(context.Background(), old)
	require.NoError(t, err)
	require.Empty(t, token)
}

type supplierRevisionTransport func(*http.Request) (*http.Response, error)

func (f supplierRevisionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSupplierTokenRotationNamespacesStaleWriter(t *testing.T) {
	old := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old", "_token_version": int64(10)}}
	latest := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "new", "_token_version": int64(11), "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	cache := newOpenAITokenCacheStub()
	// A reader may pass its DB version check before rotation commits, then populate cache afterward.
	require.NoError(t, cache.SetAccessToken(context.Background(), OpenAITokenCacheKey(old), "old", time.Hour))
	provider := NewOpenAITokenProvider(&supplierRevisionAccountRepo{a: latest}, cache, nil)
	token, err := provider.GetAccessToken(context.Background(), latest)
	require.NoError(t, err)
	require.Equal(t, "new", token)
	token, err = provider.GetAccessToken(context.Background(), old)
	require.NoError(t, err)
	// Explicit invalidation removes warm old namespaces after commit.
	require.NoError(t, NewCompositeTokenCacheInvalidator(cache).InvalidateToken(context.Background(), old))
	token, err = provider.GetAccessToken(context.Background(), old)
	require.NoError(t, err)
	require.Equal(t, "new", token)
	require.NotEqual(t, OpenAITokenCacheKey(old), OpenAITokenCacheKey(latest))
}

func TestSupplierInactiveProxyCompatibility(t *testing.T) {
	p := &Proxy{Name: "inactive", Protocol: "http", Host: "8.8.8.8", Port: 80, Status: "inactive"}
	applySupplierProxyInput(p, SupplierProxyInput{Name: supplierRevisionPtr("renamed"), Password: supplierRevisionPtr("new")})
	require.NoError(t, validateSupplierProxy(p))
	p.Status = "disabled"
	require.Error(t, validateSupplierProxy(p))
}
func supplierRevisionPtr[T any](v T) *T { return &v }

type supplierRevisionAccountRepo struct {
	AccountRepository
	a *Account
}

func (r *supplierRevisionAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.a, nil
}

type supplierRotationRepo struct {
	SupplierRepository
	a         *Account
	committed bool
}

func (r *supplierRotationRepo) GetAccount(context.Context, int64, int64) (*Account, error) {
	return r.a, nil
}
func (r *supplierRotationRepo) UpdateAccount(_ context.Context, _ int64, _ int64, in SupplierAccountUpdate) (*Account, error) {
	r.committed = true
	next := *r.a
	next.Credentials = mergeMap(r.a.Credentials, in.Credentials)
	next.Credentials["_token_version"] = int64(11)
	r.a = &next
	return r.a, nil
}

type supplierCommitInvalidator struct {
	repo     *supplierRotationRepo
	t        *testing.T
	versions []int64
}

func (i *supplierCommitInvalidator) InvalidateToken(_ context.Context, a *Account) error {
	require.True(i.t, i.repo.committed, "invalidation must follow successful persistence")
	i.versions = append(i.versions, a.GetCredentialAsInt64("_token_version"))
	return nil
}
func TestSupplierRotationInvalidatesAfterCommit(t *testing.T) {
	repo := &supplierRotationRepo{a: &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old", "_token_version": int64(10)}}}
	inv := &supplierCommitInvalidator{repo: repo, t: t}
	svc := NewSupplierService(repo, inv)
	_, err := svc.UpdateAccount(context.Background(), 11, 7, SupplierAccountUpdate{Credentials: map[string]any{"access_token": "new"}})
	require.NoError(t, err)
	require.Equal(t, []int64{10, 11}, inv.versions)
	_, err = svc.UpdateAccount(context.Background(), 11, 7, SupplierAccountUpdate{Credentials: map[string]any{"_token_version": int64(999)}})
	require.ErrorIs(t, err, ErrSupplierInvalid)
	require.Len(t, inv.versions, 2)
}
