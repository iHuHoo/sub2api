//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const noCreditClientBeta = "claude-code-20250219,context-1m-2025-08-07,fast-mode-2026-02-01,interleaved-thinking-2025-05-14"

// Permissive rules of every shape: global pass, model whitelist pass, and a 1M rule whose
// scope does not cover OAuth (which resolves to pass).
var noCreditPermissivePolicies = map[string]string{
	"pass-all": `{"rules":[{"beta_token":"context-1m-2025-08-07","action":"pass","scope":"all"},` +
		`{"beta_token":"fast-mode-2026-02-01","action":"pass","scope":"all"}]}`,
	"whitelist":      `{"rules":[{"beta_token":"context-1m-2025-08-07","action":"pass","scope":"oauth","model_whitelist":["claude-sonnet-4-6"]}]}`,
	"scope-mismatch": `{"rules":[{"beta_token":"context-1m-2025-08-07","action":"block","scope":"apikey"}]}`,
}

func requireNoCreditBetasAbsent(t *testing.T, beta string) {
	t.Helper()
	require.NotContains(t, beta, claude.BetaContext1M)
	require.NotContains(t, beta, claude.BetaFastMode)
	require.Contains(t, beta, claude.BetaInterleavedThinking, "other client betas are kept")
}

// Subscription OAuth never sends the credit-gated betas, native or mimic, messages or
// count_tokens, even with no policy filtering them at all.
func TestNoCreditOAuth_FinalHeadersDropCreditBetas(t *testing.T) {
	withTestClientProfile(t)
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	for _, mimic := range []bool{true, false} {
		for _, endpoint := range []string{"messages", "count_tokens"} {
			t.Run(endpoint+map[bool]string{true: "/mimic", false: "/native"}[mimic], func(t *testing.T) {
				resetGatewayForwardingSettingsCacheForTest(t)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				c.Request.Header.Set("anthropic-beta", noCreditClientBeta)
				svc := &GatewayService{cfg: &config.Config{}}
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
				var req *http.Request
				var err error
				if endpoint == "messages" {
					req, _, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "tok", "oauth", "claude-sonnet-4-6", false, mimic)
				} else {
					req, _, err = svc.buildCountTokensRequest(context.Background(), c, account, body, "tok", "oauth", "claude-sonnet-4-6", mimic)
				}
				require.NoError(t, err)
				beta := getHeaderRaw(req.Header, "anthropic-beta")
				if mimic && endpoint == "messages" {
					require.NotContains(t, beta, claude.BetaContext1M)
					require.NotContains(t, beta, claude.BetaFastMode)
					return
				}
				requireNoCreditBetasAbsent(t, beta)
				if endpoint == "count_tokens" {
					require.Contains(t, beta, claude.BetaTokenCounting)
				}
			})
		}
	}
}

// API-key accounts keep their pass-through contract.
func TestNoCreditOAuth_APIKeyKeepsCreditBetas(t *testing.T) {
	resetGatewayForwardingSettingsCacheForTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("anthropic-beta", noCreditClientBeta)
	svc := &GatewayService{cfg: &config.Config{}}
	account := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}}
	req, _, err := svc.buildUpstreamRequest(context.Background(), c, account,
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`), "k", "apikey", "claude-sonnet-4-6", false, false)
	require.NoError(t, err)
	require.Equal(t, noCreditClientBeta, getHeaderRaw(req.Header, "anthropic-beta"))
}

// No permissive rule opens 1M on subscription OAuth: the standard-window admission still
// rejects an oversized Sonnet 4.6 request locally, and a small one goes out without the
// credit-gated betas.
func TestNoCreditOAuth_PermissiveRulesDoNotOpenLongContext(t *testing.T) {
	for name, policy := range noCreditPermissivePolicies {
		t.Run(name, func(t *testing.T) {
			upstream := countOK("1")
			svc := newLongContextService(upstream, policy, nil)
			c, _ := longContextContext("/v1/messages", noCreditClientBeta)
			_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), textMessages("claude-sonnet-4-6", 1024, 3_000_000))
			requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
			require.Equal(t, BetaPolicyActionFilter, observationOf(t, c).Action)
			require.Zero(t, upstream.inferCalls)

			upstream = countOK("1")
			svc = newLongContextService(upstream, policy, nil)
			c, _ = longContextContext("/v1/messages", noCreditClientBeta)
			_, err = forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
			require.NoError(t, err)
			require.Len(t, upstream.inferBetas, 1)
			require.NotContains(t, upstream.inferBetas[0], claude.BetaContext1M)
			require.NotContains(t, upstream.inferBetas[0], claude.BetaFastMode)
		})
	}
}

// A native 1M model keeps its own window: no blanket 200K cap from the no-credit rule.
func TestNoCreditOAuth_Native1MModelKeepsItsWindow(t *testing.T) {
	upstream := countOK("1")
	svc := newLongContextService(upstream, noCreditPermissivePolicies["pass-all"], nil)
	c, _ := longContextContext("/v1/messages", "")
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), textMessages("claude-sonnet-5-5", 1024, 1_200_000))
	require.NoError(t, err)
	require.Equal(t, 1, upstream.inferCalls)
}

// speed=fast is stopped before dispatch on subscription OAuth, with or without the marker
// and whatever the rules say; other speeds and API-key accounts are untouched.
func TestNoCreditOAuth_FastSpeedStoppedLocally(t *testing.T) {
	apiKey := &Account{ID: 202, Name: "anthropic-apikey", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "k", "base_url": "https://api.anthropic.com"}, Status: StatusActive, Schedulable: true}
	fast := `{"model":"claude-opus-5-5","max_tokens":1024,"speed":"fast","messages":[{"role":"user","content":"hello"}]}`
	for _, tc := range []struct {
		name, beta, body string
		account          *Account
		wantCalls        int
	}{
		{"oauth no marker", "", fast, newAnthropicOAuthAccountForPartialUsageTest(), 0},
		{"oauth with marker", claude.BetaFastMode, fast, newAnthropicOAuthAccountForPartialUsageTest(), 0},
		{"oauth standard speed", "", strings.Replace(fast, `"fast"`, `"standard"`, 1), newAnthropicOAuthAccountForPartialUsageTest(), 1},
		{"oauth no speed", "", strings.Replace(fast, `"speed":"fast",`, "", 1), newAnthropicOAuthAccountForPartialUsageTest(), 1},
		{"api key fast", "", fast, apiKey, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := countOK("1")
			svc := newLongContextService(upstream, noCreditPermissivePolicies["pass-all"], nil)
			c, rec := longContextContext("/v1/messages", tc.beta)
			_, err := forwardMessages(t, svc, c, tc.account, tc.body)
			require.Equal(t, tc.wantCalls, upstream.inferCalls)
			if tc.wantCalls == 0 {
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Contains(t, rec.Body.String(), "fast mode requires usage credits")
				require.Zero(t, upstream.countCalls)
				return
			}
			require.NoError(t, err)
		})
	}
}

// Converted responses/chat requests cannot carry speed: their Anthropic request type has no
// such field, so the guard in Forward is the only place it can arrive.
func TestNoCreditOAuth_ConvertedRequestHasNoSpeedField(t *testing.T) {
	typ := reflect.TypeOf(apicompat.AnthropicRequest{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		require.NotEqual(t, "speed", name)
	}
}
