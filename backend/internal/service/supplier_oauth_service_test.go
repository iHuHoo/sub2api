//go:build unit

package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"net/url"
	"testing"
	"time"
)

func TestSupplierOAuthSessionBoundary(t *testing.T) {
	s := &SupplierOAuthService{sessions: map[string]supplierOAuthSession{"supplier-session": {owner: 11, platform: PlatformOpenAI, internalID: "admin-internal", expires: time.Now().Add(time.Minute)}}}
	for _, tc := range []struct {
		owner        int64
		platform, id string
	}{{22, PlatformOpenAI, "supplier-session"}, {11, PlatformGrok, "supplier-session"}, {11, PlatformOpenAI, "admin-internal"}} {
		_, err := s.takeSession(tc.owner, tc.platform, tc.id)
		require.ErrorIs(t, err, ErrSupplierInvalid)
	}
	got, err := s.takeSession(11, PlatformOpenAI, "supplier-session")
	require.NoError(t, err)
	require.Equal(t, "admin-internal", got.internalID)
	_, err = s.takeSession(11, PlatformOpenAI, "supplier-session")
	require.ErrorIs(t, err, ErrSupplierInvalid)
}

func TestSupplierOAuthOpenAIExchangeHappyPathAndAdminIsolation(t *testing.T) {
	client := &openaiOAuthClientStateStub{}
	upstream := NewOpenAIOAuthService(nil, client)
	defer upstream.Stop()
	s := NewSupplierOAuthService(nil, upstream, nil, nil, nil, nil)
	result, err := s.Execute(context.Background(), 11, PlatformOpenAI, "generate-auth-url", SupplierOAuthInput{})
	require.NoError(t, err)
	generated := result.(map[string]string)
	_, ok := upstream.sessionStore.Get(generated["session_id"])
	require.False(t, ok, "supplier session must not be exchangeable through admin service")
	authURL, err := url.Parse(generated["auth_url"])
	require.NoError(t, err)
	input := SupplierOAuthInput{SessionID: generated["session_id"], Code: "code", State: authURL.Query().Get("state")}
	_, err = s.Execute(context.Background(), 22, PlatformOpenAI, "exchange-code", input)
	require.ErrorIs(t, err, ErrSupplierInvalid)
	require.Zero(t, client.exchangeCalled)
	result, err = s.Execute(context.Background(), 11, PlatformOpenAI, "exchange-code", input)
	require.NoError(t, err)
	require.Equal(t, "at", result.(*OpenAITokenInfo).AccessToken)
	require.EqualValues(t, 1, client.exchangeCalled)
	_, err = s.Execute(context.Background(), 11, PlatformOpenAI, "exchange-code", input)
	require.ErrorIs(t, err, ErrSupplierInvalid)
	require.EqualValues(t, 1, client.exchangeCalled)
}

func TestSupplierOAuthExpiredSession(t *testing.T) {
	s := &SupplierOAuthService{sessions: map[string]supplierOAuthSession{"expired": {owner: 11, platform: PlatformOpenAI, expires: time.Now().Add(-time.Minute)}}}
	_, err := s.takeSession(11, PlatformOpenAI, "expired")
	require.ErrorIs(t, err, ErrSupplierInvalid)
}

type supplierRefreshClient struct {
	OpenAIOAuthClient
	proxy, clientID string
	calls           int
}

func (c *supplierRefreshClient) RefreshTokenWithClientID(_ context.Context, token, proxy, id string) (*openai.TokenResponse, error) {
	c.calls++
	c.proxy = proxy
	c.clientID = id
	return &openai.TokenResponse{AccessToken: "new-at", RefreshToken: token, ExpiresIn: 3600}, nil
}

type supplierOwnedProxyRepo struct {
	SupplierRepository
	p *Proxy
}

func (r *supplierOwnedProxyRepo) GetProxy(_ context.Context, owner, id int64) (*Proxy, error) {
	if r.p == nil || r.p.SupplierUserID == nil || *r.p.SupplierUserID != owner {
		return nil, ErrSupplierUnavailable
	}
	return r.p, nil
}
func TestSupplierOAuthOwnedRefreshAndRevokedSessionProxy(t *testing.T) {
	owner, id := int64(11), int64(9)
	repo := &supplierOwnedProxyRepo{p: &Proxy{ID: id, Name: "own", Host: "8.8.8.8", Port: 1080, Protocol: "socks5", Status: StatusActive, SupplierUserID: &owner}}
	client := &supplierRefreshClient{}
	upstream := NewOpenAIOAuthService(nil, client)
	defer upstream.Stop()
	s := NewSupplierOAuthService(NewSupplierService(repo, nil, nil), upstream, nil, nil, nil, nil)
	out, err := s.Execute(context.Background(), owner, PlatformOpenAI, "refresh-token", SupplierOAuthInput{ProxyID: &id, RefreshToken: "rt"})
	require.NoError(t, err)
	require.Equal(t, "new-at", out.(*OpenAITokenInfo).AccessToken)
	require.Equal(t, repo.p.URL(), client.proxy)
	require.Equal(t, openai.ClientID, client.clientID)
	s.sessions["saved"] = supplierOAuthSession{owner: owner, platform: PlatformOpenAI, internalID: "hidden", proxyID: &id, expires: time.Now().Add(time.Minute)}
	repo.p = nil
	_, err = s.Execute(context.Background(), owner, PlatformOpenAI, "exchange-code", SupplierOAuthInput{SessionID: "saved", Code: "code", State: "state"})
	require.ErrorIs(t, err, ErrSupplierUnavailable)
	require.Equal(t, 1, client.calls)
}

func TestSupplierOAuthWrongStateNeverExchanges(t *testing.T) {
	client := &openaiOAuthClientStateStub{}
	upstream := NewOpenAIOAuthService(nil, client)
	defer upstream.Stop()
	s := NewSupplierOAuthService(nil, upstream, nil, nil, nil, nil)
	result, err := s.Execute(context.Background(), 11, PlatformOpenAI, "generate-auth-url", SupplierOAuthInput{})
	require.NoError(t, err)
	_, err = s.Execute(context.Background(), 11, PlatformOpenAI, "exchange-code", SupplierOAuthInput{SessionID: result.(map[string]string)["session_id"], Code: "code", State: "wrong"})
	require.Error(t, err)
	require.Zero(t, client.exchangeCalled)
}

func TestSupplierGrokAuthURLReturnsState(t *testing.T) {
	grok := NewGrokOAuthService(nil, nil)
	defer grok.Stop()
	s := NewSupplierOAuthService(nil, nil, nil, nil, nil, grok)
	result, err := s.Execute(context.Background(), 11, PlatformGrok, "generate-auth-url", SupplierOAuthInput{})
	require.NoError(t, err)
	generated := result.(map[string]string)
	parsed, err := url.Parse(generated["auth_url"])
	require.NoError(t, err)
	require.NotEmpty(t, generated["state"])
	require.Equal(t, parsed.Query().Get("state"), generated["state"])
}

func TestSupplierAuthModeCredentialsRemainConstrained(t *testing.T) {
	require.NoError(t, validateSupplierCredentials(PlatformGemini, AccountTypeAPIKey, map[string]any{"api_key": "key", "tier_id": GeminiTierAIStudioPaid}, true))
	require.NoError(t, validateSupplierCredentials(PlatformOpenAI, AccountTypeOAuth, map[string]any{"access_token": "at", "plan_type": "plus"}, true))
	require.NoError(t, validateSupplierCredentials(PlatformAntigravity, AccountTypeOAuth, map[string]any{"access_token": "at", "plan_type": "Ultra"}, true))
	require.ErrorIs(t, validateSupplierCredentials(PlatformGemini, AccountTypeAPIKey, map[string]any{"api_key": "key", "tier_id": "arbitrary"}, true), ErrSupplierInvalid)
	require.ErrorIs(t, validateSupplierCredentials(PlatformOpenAI, AccountTypeOAuth, map[string]any{"access_token": "at", "plan_type": "arbitrary"}, true), ErrSupplierInvalid)
	require.NoError(t, validateSupplierCredentials(PlatformGemini, AccountTypeOAuth, map[string]any{"access_token": "at", "refresh_token": "rt", "oauth_type": "google_one", "tier_id": "google_one_free"}, true))
	require.NoError(t, validateSupplierCredentials(PlatformKimi, AccountTypeAPIKey, map[string]any{"api_key": "key", "account_mode": AccountModeCoding, "api_protocol": APIProtocolAnthropic, "api_base_urls": map[string]any{APIProtocolAnthropic: DefaultKimiCodingAnthropicBaseURL}}, true))
	for _, creds := range []map[string]any{{"api_key": "key", "api_base_urls": map[string]any{APIProtocolAnthropic: "http://127.0.0.1"}}, {"api_key": "key", "api_protocol": "unsafe"}, {"api_key": "key", "account_mode": "unsafe"}} {
		require.ErrorIs(t, validateSupplierCredentials(PlatformKimi, AccountTypeAPIKey, creds, true), ErrSupplierInvalid)
	}
	require.ErrorIs(t, validateSupplierCredentials(PlatformOpenAI, AccountTypeOAuth, map[string]any{"access_token": "at", "token_uri": "http://localhost"}, true), ErrSupplierInvalid)
}

type supplierOAuthProxyRepo struct {
	SupplierRepository
	calls int
}

func (r *supplierOAuthProxyRepo) GetProxy(context.Context, int64, int64) (*Proxy, error) {
	r.calls++
	return nil, ErrSupplierUnavailable
}
func TestSupplierOAuthRejectsForeignProxyBeforeUpstream(t *testing.T) {
	r := &supplierOAuthProxyRepo{}
	s := &SupplierOAuthService{supplier: NewSupplierService(r, nil, nil)}
	id := int64(9)
	_, err := s.Execute(context.Background(), 11, PlatformOpenAI, "refresh-token", SupplierOAuthInput{ProxyID: &id, RefreshToken: "secret"})
	require.ErrorIs(t, err, ErrSupplierUnavailable)
	require.Equal(t, 1, r.calls)
}
