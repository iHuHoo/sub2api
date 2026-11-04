//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// longContextUpstream answers count_tokens with a controlled count and everything else
// with a successful inference, and records which kind of call was made.
type longContextUpstream struct {
	mu          sync.Mutex
	countStatus int
	countBody   string
	countErr    error
	countDelay  time.Duration
	countCalls  int
	inferCalls  int
	countBodies [][]byte
	countURLs   []string
	countSeq    []string         // per-call input_tokens, overrides countBody
	inferResps  []*http.Response // per-call inference responses before the default success
}

func (u *longContextUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, concurrency, nil)
}

func (u *longContextUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	if strings.Contains(req.URL.Path, "count_tokens") {
		u.countCalls++
		if !HTTPUpstreamRedirectsDisabled(req.Context()) {
			return nil, errors.New("count request must disable redirects")
		}
		u.countBodies = append(u.countBodies, body)
		u.countURLs = append(u.countURLs, req.URL.String())
		if u.countDelay > 0 {
			select {
			case <-time.After(u.countDelay):
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		if u.countErr != nil {
			return nil, u.countErr
		}
		if i := u.countCalls - 1; i < len(u.countSeq) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":` + u.countSeq[i] + `}`))}, nil
		}
		return &http.Response{StatusCode: u.countStatus, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(u.countBody))}, nil
	}
	u.inferCalls++
	if i := u.inferCalls - 1; i < len(u.inferResps) {
		return u.inferResps[i], nil
	}
	if gjson.GetBytes(body, "stream").Bool() {
		sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-6\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(
		`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
}

func countOK(tokens string) *longContextUpstream {
	return &longContextUpstream{countStatus: http.StatusOK, countBody: `{"input_tokens":` + tokens + `}`}
}

type longContextSettingRepo struct {
	betaPolicySettingRepoStub
	err error
}

func (r *longContextSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.err != nil && key == SettingKeyBetaPolicySettings {
		return "", r.err
	}
	return r.betaPolicySettingRepoStub.GetValue(ctx, key)
}

func (r *longContextSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// policy "" means no stored setting (default).
func newLongContextService(upstream *longContextUpstream, policy string, settingsErr error) *GatewayService {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	values := map[string]string{}
	if policy != "" {
		values[SettingKeyBetaPolicySettings] = policy
	}
	return &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
		tlsFPProfileService:  &TLSFingerprintProfileService{},
		settingService:       NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub{values: values}, settingsErr}, cfg),
	}
}

func longContextContext(path, beta string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	if beta != "" {
		c.Request.Header.Set("anthropic-beta", beta)
	}
	return c, rec
}

func forwardMessages(t *testing.T, svc *GatewayService, c *gin.Context, account *Account, body string) (*ForwardResult, error) {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
	require.NoError(t, err)
	return svc.Forward(context.Background(), c, account, parsed)
}

func requireLongContextStop(t *testing.T, err error, reason GatewayFailureReason, status int) *UpstreamFailoverError {
	t.Helper()
	var stop *UpstreamFailoverError
	require.ErrorAs(t, err, &stop)
	require.True(t, stop.IsLongContextStop(), "reason=%s", stop.Reason)
	require.False(t, stop.ShouldRetryNextAccount())
	require.Equal(t, reason, stop.Reason)
	require.Equal(t, status, stop.ClientStatusCode)
	require.Empty(t, stop.ResponseHeaders, "no Retry-After or upstream headers")
	return stop
}

const smallMessages = `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`

const (
	policyBlockAll   = `{"rules":[{"beta_token":"context-1m-2025-08-07","action":"block","scope":"all"}]}`
	policyBlockOAuth = `{"rules":[{"beta_token":"context-1m-2025-08-07","action":"block","scope":"oauth"}]}`
	policyFilterAll  = `{"rules":[{"beta_token":"context-1m-2025-08-07","action":"filter","scope":"all"}]}`
	policyPassAll    = `{"rules":[{"beta_token":"context-1m-2025-08-07","action":"pass","scope":"all"}]}`
)

// The #139 reproduction, inverted: without the 1M marker an oversized request (by a
// deterministic counter result, not by its byte size) must not reach inference.
func TestLongContextAdmission_NoMarkerOversizedNeverReachesInference(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
	}{{"default (no setting)", ""}, {"block oauth (production shape)", policyBlockOAuth}, {"filter", policyFilterAll}} {
		t.Run(tc.name, func(t *testing.T) {
			// budget = 200000 - 1024 - 8192 = 190784
			upstream := countOK("190785")
			svc := newLongContextService(upstream, tc.policy, nil)
			c, _ := longContextContext("/v1/messages", "")
			_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
			requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
			require.Equal(t, 1, upstream.countCalls)
			require.Zero(t, upstream.inferCalls)
			require.False(t, gjson.GetBytes(upstream.countBodies[0], "max_tokens").Exists(), "counter gets a generation-sanitized copy")
		})
	}
}

func TestLongContextAdmission_AtBudgetReachesInference(t *testing.T) {
	upstream := countOK("190784")
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
	require.NoError(t, err)
	require.Equal(t, 1, upstream.countCalls)
	require.Equal(t, 1, upstream.inferCalls)
}

func TestLongContextAdmission_MarkerBlockedWithoutAnyCall(t *testing.T) {
	upstream := countOK("1")
	svc := newLongContextService(upstream, policyBlockAll, nil)
	c, _ := longContextContext("/v1/messages", "context-1m-2025-08-07,interleaved-thinking-2025-05-14")
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
	stop := requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
	require.Contains(t, stop.ClientMessage, "1M")
	require.Zero(t, upstream.countCalls+upstream.inferCalls)
}

func TestLongContextAdmission_FilterStripsMarkerAndStillChecksLength(t *testing.T) {
	upstream := countOK("10")
	svc := newLongContextService(upstream, policyFilterAll, nil)
	c, _ := longContextContext("/v1/messages", "context-1m-2025-08-07")
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
	require.NoError(t, err)
	require.Equal(t, 1, upstream.countCalls, "filter is not permission")
	require.Equal(t, 1, upstream.inferCalls)
}

func TestLongContextAdmission_PassAndOutOfScopeSkipTheGate(t *testing.T) {
	apiKey := &Account{ID: 202, Name: "anthropic-apikey", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "k", "base_url": "https://api.anthropic.com"}, Status: StatusActive, Schedulable: true}
	for _, tc := range []struct {
		name, policy string
		account      *Account
	}{
		{"pass opt-in", policyPassAll, newAnthropicOAuthAccountForPartialUsageTest()},
		{"oauth-only block does not cover api key", policyBlockOAuth, apiKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := countOK("999999")
			svc := newLongContextService(upstream, tc.policy, nil)
			c, _ := longContextContext("/v1/messages", "")
			_, err := forwardMessages(t, svc, c, tc.account, smallMessages)
			require.NoError(t, err)
			require.Zero(t, upstream.countCalls)
			require.Equal(t, 1, upstream.inferCalls)
		})
	}
}

func TestLongContextAdmission_OutputBudgetAloneRejectsLocally(t *testing.T) {
	upstream := countOK("1")
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	body := `{"model":"claude-sonnet-4-6","max_tokens":191809,"messages":[{"role":"user","content":"hi"}]}`
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
	requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
	require.Zero(t, upstream.countCalls+upstream.inferCalls)
}

func TestLongContextAdmission_CounterFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream *longContextUpstream
		status   int
	}{
		{"counter 429 is transient", &longContextUpstream{countStatus: 429, countBody: `{"type":"error","error":{"type":"rate_limit_error","message":"x"}}`}, 503},
		{"counter 500 is transient", &longContextUpstream{countStatus: 500, countBody: `{}`}, 503},
		{"counter transport error is transient", &longContextUpstream{countErr: errors.New("dial tcp: refused")}, 503},
		{"counter corrupt output is transient", &longContextUpstream{countStatus: 200, countBody: `{"input_tokens":-3}`}, 503},
		{"counter 400 is permanent", &longContextUpstream{countStatus: 400, countBody: `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`}, 400},
		{"counter 404 is permanent", &longContextUpstream{countStatus: 404, countBody: `{"type":"error","error":{"type":"not_found_error","message":"Not found"}}`}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newLongContextService(tc.upstream, "", nil)
			c, _ := longContextContext("/v1/messages", "")
			_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
			stop := requireLongContextStop(t, err, GatewayFailureReasonLongContextCountUnavailable, tc.status)
			if tc.status == 503 {
				require.Equal(t, "api_error", stop.ClientErrorType())
			} else {
				require.Equal(t, "invalid_request_error", stop.ClientErrorType())
			}
			require.Equal(t, 1, tc.upstream.countCalls, "no counting retry")
			require.Zero(t, tc.upstream.inferCalls)
		})
	}
}

func TestLongContextAdmission_CountBudgetBoundsWallTime(t *testing.T) {
	upstream := countOK("1")
	upstream.countDelay = time.Minute
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	state := longContextStateFor(c)
	state.spent = longContextCountBudget - 50*time.Millisecond
	started := time.Now()
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
	requireLongContextStop(t, err, GatewayFailureReasonLongContextCountUnavailable, http.StatusServiceUnavailable)
	require.Less(t, time.Since(started), 5*time.Second)
	require.Zero(t, upstream.inferCalls)

	// Exhausted budget: no further counter call at all.
	c2, _ := longContextContext("/v1/messages", "")
	longContextStateFor(c2).spent = longContextCountBudget
	upstream2 := countOK("1")
	svc2 := newLongContextService(upstream2, "", nil)
	_, err = forwardMessages(t, svc2, c2, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
	requireLongContextStop(t, err, GatewayFailureReasonLongContextCountUnavailable, http.StatusServiceUnavailable)
	require.Zero(t, upstream2.countCalls+upstream2.inferCalls)
}

func TestLongContextAdmission_PolicyReadFailureIsNeverAPass(t *testing.T) {
	upstream := countOK("1")
	svc := newLongContextService(upstream, "", errors.New("db down"))
	c, _ := longContextContext("/v1/messages", "")
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
	stop := requireLongContextStop(t, err, GatewayFailureReasonLongContextPolicyUnavailable, http.StatusServiceUnavailable)
	require.Equal(t, "api_error", stop.ClientErrorType())
	require.Zero(t, upstream.countCalls+upstream.inferCalls)
}

func TestLongContextAdmission_CorruptOrLegacySettingsFallBackToBlock(t *testing.T) {
	legacy := `{"rules":[{"beta_token":"context-1m-2025-08-07","action":"pass","scope":"all","model_whitelist":["claude-sonnet-5","claude-sonnet-5-*","claude-sonnet-5@*","us.anthropic.claude-sonnet-5*","eu.anthropic.claude-sonnet-5*","apac.anthropic.claude-sonnet-5*","jp.anthropic.claude-sonnet-5*","au.anthropic.claude-sonnet-5*","us-gov.anthropic.claude-sonnet-5*","global.anthropic.claude-sonnet-5*","anthropic.claude-sonnet-5*"],"fallback_action":"filter"}]}`
	otherOnly := `{"rules":[{"beta_token":"fast-mode-2026-02-01","action":"filter","scope":"all"}]}`
	for name, policy := range map[string]string{"corrupt json": `{not json`, "legacy generated default": legacy, "no 1M rule": otherOnly} {
		t.Run(name, func(t *testing.T) {
			upstream := countOK("190785")
			svc := newLongContextService(upstream, policy, nil)
			c, _ := longContextContext("/v1/messages", "")
			body := strings.Replace(smallMessages, "claude-sonnet-4-6", "claude-sonnet-5", 1)
			_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
			requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
			require.Zero(t, upstream.inferCalls)
		})
	}
}

func TestLongContextAdmission_CalibratedProfileDecidesLocally(t *testing.T) {
	saved := longContextProfiles
	t.Cleanup(func() { longContextProfiles = saved })
	// Controlled profile: tokens within [B/10, B/2].
	longContextProfiles = []longContextProfile{{Name: "test", Route: longContextRouteAnthropicOAuth, Models: []string{"claude-sonnet-4-6"}, LowNum: 1, LowDen: 10, HighNum: 1, HighDen: 2}}

	t.Run("small text admitted without a count", func(t *testing.T) {
		upstream := countOK("999999")
		svc := newLongContextService(upstream, "", nil)
		c, _ := longContextContext("/v1/messages", "")
		_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
		require.NoError(t, err)
		require.Zero(t, upstream.countCalls)
		require.Equal(t, 1, upstream.inferCalls)
	})
	t.Run("huge text rejected without a count", func(t *testing.T) {
		upstream := countOK("1")
		svc := newLongContextService(upstream, "", nil)
		c, _ := longContextContext("/v1/messages", "")
		body := `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"` + strings.Repeat("abcdefghij", 200_000) + `"}]}`
		_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
		requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
		require.Zero(t, upstream.countCalls+upstream.inferCalls)
	})
	t.Run("image is not locally eligible", func(t *testing.T) {
		upstream := countOK("10")
		svc := newLongContextService(upstream, "", nil)
		c, _ := longContextContext("/v1/messages", "")
		body := `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},{"type":"text","text":"what"}]}]}`
		_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
		require.NoError(t, err)
		require.Equal(t, 1, upstream.countCalls)
	})
	t.Run("other model does not inherit the profile", func(t *testing.T) {
		upstream := countOK("10")
		svc := newLongContextService(upstream, "", nil)
		c, _ := longContextContext("/v1/messages", "")
		_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), strings.Replace(smallMessages, "claude-sonnet-4-6", "claude-opus-4-6", 1))
		require.NoError(t, err)
		require.Equal(t, 1, upstream.countCalls)
	})
}

func TestLongContextAdmission_RoutesWithoutCounterRefuseUncertain(t *testing.T) {
	upstream := countOK("1")
	svc := newLongContextService(upstream, "", nil)
	vertex := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeServiceAccount}
	bedrock := &Account{ID: 10, Platform: PlatformAnthropic, Type: AccountTypeBedrock}
	for _, a := range []*Account{vertex, bedrock} {
		require.Equal(t, longContextRouteNoCounter, longContextRouteFor(a))
		c, _ := longContextContext("/v1/messages", "")
		err := svc.admitLongContext(context.Background(), c, longContextAttempt{Entry: "test", Account: a, Model: "claude-sonnet-4-6", Body: []byte(smallMessages), Route: longContextRouteFor(a)})
		requireLongContextStop(t, err, GatewayFailureReasonLongContextCountUnavailable, http.StatusBadRequest)
	}
	require.Zero(t, upstream.countCalls+upstream.inferCalls, "a Google credential is never sent to the Anthropic counter")
}

func TestLongContextAdmission_IdenticalAttemptCountedOnce(t *testing.T) {
	upstream := countOK("10")
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	account := newAnthropicOAuthAccountForPartialUsageTest()
	a := svc.anthropicLongContextAttempt(c, "messages", account, "claude-sonnet-4-6", []byte(smallMessages), http.Header{}, "tok", "oauth", false, "")
	require.NoError(t, svc.admitLongContext(context.Background(), c, a))
	require.NoError(t, svc.admitLongContext(context.Background(), c, a))
	require.Equal(t, 1, upstream.countCalls)

	a.Body = []byte(strings.Replace(smallMessages, "hello", "hello again", 1))
	require.NoError(t, svc.admitLongContext(context.Background(), c, a))
	require.Equal(t, 2, upstream.countCalls, "changed input is recounted")
}

func TestLongContextAdmission_CompatAdapters(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		call       func(*GatewayService, *gin.Context, *Account) (*ForwardResult, error)
	}{
		{"chat completions", "/v1/chat/completions", func(svc *GatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return svc.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}],"stream":true}`), nil)
		}},
		{"responses", "/v1/responses", func(svc *GatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return svc.ForwardAsResponses(context.Background(), c, a, []byte(`{"model":"claude-sonnet-4-6","input":"hello"}`), nil)
		}},
	} {
		t.Run(tc.name+" oversized", func(t *testing.T) {
			upstream := countOK("199999")
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext(tc.path, "")
			result, err := tc.call(svc, c, newAnthropicOAuthAccountForPartialUsageTest())
			require.Nil(t, result)
			requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
			require.Equal(t, 1, upstream.countCalls)
			require.Zero(t, upstream.inferCalls)
			require.True(t, gjson.GetBytes(upstream.countBodies[0], "messages").Exists(), "the converted Anthropic body is counted")
		})
		t.Run(tc.name+" normal", func(t *testing.T) {
			upstream := countOK("50")
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext(tc.path, "")
			_, err := tc.call(svc, c, newAnthropicOAuthAccountForPartialUsageTest())
			require.NoError(t, err)
			require.Equal(t, 1, upstream.countCalls)
			require.Equal(t, 1, upstream.inferCalls)
		})
	}
}

func TestLongContextAdmission_APIKeyPassthroughUsesItsCounter(t *testing.T) {
	upstream := countOK("190785")
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	_, err := forwardMessages(t, svc, c, newAnthropicAPIKeyAccountForTest(), smallMessages)
	requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
	require.Equal(t, 1, upstream.countCalls)
	require.Zero(t, upstream.inferCalls)
}

func TestDecideLongContextLocallyBoundaries(t *testing.T) {
	p := &longContextProfile{LowNum: 1, LowDen: 4, HighNum: 1, HighDen: 2}
	// U = ceil(B/2) + 8192 <= T admits.
	require.Equal(t, longContextAdmit, decideLongContextLocally(2000, 1000+8192, p))
	require.Equal(t, longContextUncertain, decideLongContextLocally(2001, 1000+8192, p))
	// L = floor(B/4) - 8192 > T rejects.
	require.Equal(t, longContextReject, decideLongContextLocally(4*(8192+1001), 1000, p))
	require.Equal(t, longContextUncertain, decideLongContextLocally(4*(8192+1000), 1000, p))
	require.Equal(t, longContextUncertain, decideLongContextLocally(10, 1_000_000, nil), "no profile is never a guess")
	require.Equal(t, longContextReject, decideLongContextLocally(10, -1, nil))
	require.Equal(t, longContextUncertain, decideLongContextLocally(10, 1_000_000, &longContextProfile{HighNum: 1}), "invalid profile ignored")
}

func TestResolveLongContextAction(t *testing.T) {
	oauth := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	apiKey := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	rule := func(action, scope string) BetaPolicyRule {
		return BetaPolicyRule{BetaToken: longContextBetaToken, Action: action, Scope: scope}
	}
	require.Equal(t, BetaPolicyActionBlock, resolveLongContextAction(nil, oauth, "claude-sonnet-4-6"))
	require.Equal(t, BetaPolicyActionBlock, resolveLongContextAction(&BetaPolicySettings{}, oauth, "claude-sonnet-4-6"), "no 1M rule")
	require.Equal(t, BetaPolicyActionPass, resolveLongContextAction(&BetaPolicySettings{Rules: []BetaPolicyRule{rule("block", "oauth")}}, apiKey, "claude-sonnet-4-6"), "narrow scope kept")
	require.Equal(t, BetaPolicyActionBlock, resolveLongContextAction(&BetaPolicySettings{Rules: []BetaPolicyRule{rule("pass", "all"), rule("block", "oauth")}}, oauth, "claude-sonnet-4-6"), "duplicates: most restrictive")
	require.Equal(t, BetaPolicyActionBlock, resolveLongContextAction(&BetaPolicySettings{Rules: []BetaPolicyRule{rule("bogus", "all")}}, oauth, "claude-sonnet-4-6"), "unknown action never opts in")
	wl := BetaPolicyRule{BetaToken: longContextBetaToken, Action: "pass", Scope: "all", ModelWhitelist: []string{"claude-opus-4-6"}, FallbackAction: "filter"}
	require.Equal(t, BetaPolicyActionPass, resolveLongContextActionModel(&BetaPolicySettings{Rules: []BetaPolicyRule{wl}}, oauth, "claude-opus-4-6"))
	require.Equal(t, BetaPolicyActionFilter, resolveLongContextActionModel(&BetaPolicySettings{Rules: []BetaPolicyRule{wl}}, oauth, "claude-sonnet-4-6"))
}

func resolveLongContextActionModel(s *BetaPolicySettings, a *Account, model string) string {
	return resolveLongContextAction(s, a, model)
}

func TestIsLongContextLocallyEligible(t *testing.T) {
	for body, want := range map[string]bool{
		smallMessages: true,
		`{"system":[{"type":"text","text":"s"}],"tools":[{"name":"f","input_schema":{"type":"object"}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"r"}]}]}]}`: true,
		`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"s"}]}]}`:                                                         false,
		`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"x"}]}`:                                                  false,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"url","url":"https://x"}}]}]}]}`: false,
		`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","data":"x"}}]}]}`:                                                         false,
		`{"model":"x"}`: false,
	} {
		require.Equal(t, want, isLongContextLocallyEligible([]byte(body)), body)
	}
}

// The budget rectifier retry raises max_tokens (to 64000), which shrinks the input budget:
// the rewritten body is admitted again and stopped before its inference call.
func TestLongContextAdmission_RewrittenRetryIsRecounted(t *testing.T) {
	upstream := &longContextUpstream{countStatus: http.StatusOK, countSeq: []string{"130000", "130000"}, inferResps: []*http.Response{{
		StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"thinking.budget_tokens: Input should be greater than or equal to 1024"}}`)),
	}}}
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	body := `{"model":"claude-sonnet-4-6","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":512},"messages":[{"role":"user","content":"q"}]}`
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
	requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
	require.Equal(t, 2, upstream.countCalls, "initial and rewritten bodies are decided separately")
	require.Equal(t, 1, upstream.inferCalls, "the rewritten retry never reaches inference")
}

func TestSetBetaPolicyRejectsDuplicateLongContextRule(t *testing.T) {
	svc := NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{}}}, &config.Config{})
	err := svc.SetBetaPolicySettings(context.Background(), &BetaPolicySettings{Rules: []BetaPolicyRule{
		{BetaToken: longContextBetaToken, Action: "pass", Scope: "all"},
		{BetaToken: longContextBetaToken, Action: "block", Scope: "oauth"},
	}})
	require.ErrorContains(t, err, "duplicate")
}

// With a profile installed, anything outside the understood schema still goes to the
// counter: it is never admitted locally, and nothing is stripped to make it eligible.
func TestLongContextAdmission_UnknownInputsNeverDecidedLocally(t *testing.T) {
	saved := longContextProfiles
	t.Cleanup(func() { longContextProfiles = saved })
	longContextProfiles = []longContextProfile{{Name: "test", Route: longContextRouteAnthropicOAuth, Models: []string{"claude-sonnet-4-6"}, LowNum: 1, LowDen: 10, HighNum: 1, HighDen: 2}}
	head := `{"model":"claude-sonnet-4-6","max_tokens":1024,`
	for name, body := range map[string]string{
		"mcp servers":          head + `"mcp_servers":[{"type":"url","url":"https://mcp.example","name":"m"}],"messages":[{"role":"user","content":"hi"}]}`,
		"container":            head + `"container":"c_1","messages":[{"role":"user","content":"hi"}]}`,
		"unknown root field":   head + `"future_input":{"x":1},"messages":[{"role":"user","content":"hi"}]}`,
		"text with citations":  head + `"messages":[{"role":"user","content":[{"type":"text","text":"hi","citations":[{"type":"web_search_result_location"}]}]}]}`,
		"unknown message key":  head + `"messages":[{"role":"user","content":"hi","attachments":["f"]}]}`,
		"tool extra field":     head + `"tools":[{"name":"f","input_schema":{"type":"object"},"defer_loading":true}],"messages":[{"role":"user","content":"hi"}]}`,
		"server tool":          head + `"tools":[{"type":"web_fetch_20250910","name":"web_fetch"}],"messages":[{"role":"user","content":"hi"}]}`,
		"tool result document": head + `"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"document","source":{"type":"url","url":"https://x"}}]}]}]}`,
		"mcp tool use":         head + `"messages":[{"role":"assistant","content":[{"type":"mcp_tool_use","id":"m","name":"n","server_name":"s","input":{}}]}]}`,
		"container upload":     head + `"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"f"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, isLongContextLocallyEligible([]byte(body)))
			upstream := countOK("10")
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext("/v1/messages", "")
			_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
			require.NoError(t, err)
			require.Equal(t, 1, upstream.countCalls, "counted, not local_admit")
		})
	}
}

func TestLongContextAdmission_MalformedCountResponsesFailClosed(t *testing.T) {
	for name, body := range map[string]string{
		"truncated json":     `{"input_tokens":1`,
		"trailing data":      `{"input_tokens":1}{"input_tokens":2}`,
		"fractional":         `{"input_tokens":1.5}`,
		"exponent":           `{"input_tokens":1e3}`,
		"string":             `{"input_tokens":"1"}`,
		"negative":           `{"input_tokens":-1}`,
		"missing":            `{"tokens":1}`,
		"null":               `{"input_tokens":null}`,
		"overflow":           `{"input_tokens":99999999999999999999}`,
		"oversized response": `{"input_tokens":1,"pad":"` + strings.Repeat("x", longContextCountMaxResponseBytes) + `"}`,
		"not an object":      `[1]`,
		"empty":              ``,
	} {
		t.Run(name, func(t *testing.T) {
			upstream := &longContextUpstream{countStatus: http.StatusOK, countBody: body}
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext("/v1/messages", "")
			_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
			requireLongContextStop(t, err, GatewayFailureReasonLongContextCountUnavailable, http.StatusServiceUnavailable)
			require.Zero(t, upstream.inferCalls)
		})
	}
	n, err := parseLongContextCount([]byte(" {\"input_tokens\": 42, \"other\": [1,2]}\n"))
	require.NoError(t, err)
	require.Equal(t, int64(42), n)
}

// Read-time migration: repeated reads agree, the stored value is untouched until an admin
// save, and the save persists the migrated rule.
func TestLegacyLongContextDefaultMigratesOnReadAndPersistsOnSave(t *testing.T) {
	legacy, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{
		{BetaToken: "fast-mode-2026-02-01", Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll},
		legacyLongContextDefaultRule,
	}})
	require.NoError(t, err)
	repo := &longContextWritableRepo{values: map[string]string{SettingKeyBetaPolicySettings: string(legacy)}}
	svc := NewSettingService(repo, &config.Config{})
	for i := 0; i < 2; i++ {
		got, err := svc.GetBetaPolicySettings(context.Background())
		require.NoError(t, err)
		require.Equal(t, defaultLongContextRule(), got.Rules[1])
		require.Equal(t, BetaPolicyActionFilter, got.Rules[0].Action)
	}
	require.Equal(t, string(legacy), repo.values[SettingKeyBetaPolicySettings], "reads never write")
	got, _ := svc.GetBetaPolicySettings(context.Background())
	require.NoError(t, svc.SetBetaPolicySettings(context.Background(), got))
	require.Contains(t, repo.values[SettingKeyBetaPolicySettings], `"action":"block"`)
	require.NotContains(t, repo.values[SettingKeyBetaPolicySettings], "claude-sonnet-5")
}

type longContextWritableRepo struct {
	betaPolicySettingRepoStub
	values map[string]string
}

func (r *longContextWritableRepo) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (r *longContextWritableRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
