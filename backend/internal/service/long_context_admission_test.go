//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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
	inferBetas  []string // anthropic-beta of each inference call
	countBodies [][]byte
	countURLs   []string
	countSeq    []string         // per-call input_tokens, overrides countBody
	inferResps  []*http.Response // per-call inference responses before the default success
	inferErrs   []error          // per-call transport errors, checked before inferResps
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
	u.inferBetas = append(u.inferBetas, getHeaderRaw(req.Header, "anthropic-beta"))
	if i := u.inferCalls - 1; i < len(u.inferErrs) && u.inferErrs[i] != nil {
		return nil, u.inferErrs[i]
	}
	if i := u.inferCalls - 1; i < len(u.inferResps) && u.inferResps[i] != nil {
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
		billingService:       NewBillingService(cfg, longContextPricing),
	}
}

// The shipped pricing data is the window source, so tests use the real file.
var longContextPricing = func() *PricingService {
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	if err != nil {
		panic(err)
	}
	ps := &PricingService{}
	if ps.pricingData, err = ps.parsePricingData(data); err != nil {
		panic(err)
	}
	return ps
}()

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

// --- no-counter admission (#139 option C) ---------------------------------------------

// textMessages builds a plain-text request whose content is about n bytes of prose/code.
func textMessages(model string, maxTokens, n int) string {
	unit := "func handle(ctx context.Context) error { return nil } // gateway request "
	return `{"model":"` + model + `","max_tokens":` + strconv.Itoa(maxTokens) + `,"messages":[{"role":"user","content":"` + strings.Repeat(unit, n/len(unit)+1) + `"}]}`
}

func observationOf(t *testing.T, c *gin.Context) *longContextObservation {
	t.Helper()
	v, ok := c.Get(longContextObservationKey)
	require.True(t, ok, "admission recorded an observation")
	obs, _ := v.(*longContextObservation)
	require.NotNil(t, obs)
	return obs
}

// Admission never calls the counter: every decision is local.
func requireNoCount(t *testing.T, u *longContextUpstream) {
	t.Helper()
	require.Zero(t, u.countCalls, "inference admission must not call count_tokens")
}

// The #139 reproduction, inverted without a counter: a long plain-text request on a
// calibrated model is rejected locally, a medium one is decided by the realistic estimate.
func TestLongContextAdmission_LocalDecisionsAcrossTheBands(t *testing.T) {
	for _, policy := range []string{"", policyBlockOAuth, policyFilterAll} {
		for _, tc := range []struct {
			name     string
			bytes    int
			decision string
			infer    int
		}{
			{"small: profile admit", 1_000, "profile_admit", 1},
			{"600KB (over 200K tokens): profile admit", 600_000, "profile_admit", 1},
			{"1.5MB: estimate admit", 1_500_000, "estimate_admit", 1},
			{"3MB: estimate reject", 3_000_000, "estimate_reject", 0},
			{"12MB: profile reject", 12_000_000, "profile_reject", 0},
		} {
			t.Run(policy+"/"+tc.name, func(t *testing.T) {
				upstream := countOK("1")
				svc := newLongContextService(upstream, policy, nil)
				c, _ := longContextContext("/v1/messages", "")
				_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), textMessages("claude-sonnet-4-6", 1024, tc.bytes))
				obs := observationOf(t, c)
				require.Equal(t, tc.decision, obs.Decision)
				require.Equal(t, "eligible", obs.Eligibility)
				require.True(t, obs.Mimic, "a non Claude Code client is mimicked")
				if tc.infer == 0 {
					requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tc.infer, upstream.inferCalls)
				requireNoCount(t, upstream)
			})
		}
	}
}

// The estimate boundary, exactly: reject when ceil(B*0.4037)+H > T.
func TestLongContextRealisticEstimateBoundary(t *testing.T) {
	budget := longContextInputBudget(1_000_000, 1024) // 990784
	b := (budget - longContextFramingAllowance) * longContextRealisticDen / longContextRealisticNum
	for longContextRealisticEstimate(b+1) <= budget {
		b++
	}
	for longContextRealisticEstimate(b) > budget {
		b--
	}
	require.LessOrEqual(t, longContextRealisticEstimate(b), budget)
	require.Greater(t, longContextRealisticEstimate(b+1), budget)
	require.InDelta(t, 2_433_966, b, 2, "about 2.43 MB at max_tokens 1024 on a 1M model")
	require.Equal(t, int64(4037+8192), longContextRealisticEstimate(10_000))
}

// Native Claude Code requests use the same profiles (accepted transfer), recorded as mimic=false.
func TestLongContextAdmission_NativeClaudeCodeUsesProfiles(t *testing.T) {
	for _, tc := range []struct {
		bytes    int
		decision string
	}{{1_000, "profile_admit"}, {1_500_000, "estimate_admit"}, {3_000_000, "estimate_reject"}} {
		upstream := countOK("1")
		svc := newLongContextService(upstream, "", nil)
		c, _ := longContextContext("/v1/messages", "")
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(textMessages("claude-opus-4-6", 1024, tc.bytes))), PlatformAnthropic)
		require.NoError(t, err)
		_, _ = svc.Forward(SetClaudeCodeClient(context.Background(), true), c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)
		obs := observationOf(t, c)
		require.False(t, obs.Mimic)
		require.Equal(t, tc.decision, obs.Decision)
		requireNoCount(t, upstream)
	}
}

func TestLongContextAdmission_MarkerBlockedWithoutAnyCall(t *testing.T) {
	upstream := countOK("1")
	svc := newLongContextService(upstream, policyBlockAll, nil)
	c, _ := longContextContext("/v1/messages", "context-1m-2025-08-07,interleaved-thinking-2025-05-14")
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), smallMessages)
	stop := requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
	require.Contains(t, stop.ClientMessage, "1M")
	require.Equal(t, "marker_reject", observationOf(t, c).Decision)
	require.Zero(t, upstream.countCalls+upstream.inferCalls)
}

func TestLongContextAdmission_FilterStripsMarkerAndStillChecksLength(t *testing.T) {
	upstream := countOK("1")
	svc := newLongContextService(upstream, policyFilterAll, nil)
	c, _ := longContextContext("/v1/messages", "context-1m-2025-08-07")
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), textMessages("claude-sonnet-4-6", 1024, 3_000_000))
	requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
	require.Equal(t, "estimate_reject", observationOf(t, c).Decision, "filter keeps the model window, it is not permission")
	require.Zero(t, upstream.inferCalls)
	requireNoCount(t, upstream)
}

func TestLongContextAdmission_PassAndOutOfScopeSkipTheGate(t *testing.T) {
	apiKey := &Account{ID: 202, Name: "anthropic-apikey", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "k", "base_url": "https://api.anthropic.com"}, Status: StatusActive, Schedulable: true}
	for _, tc := range []struct {
		name, policy string
		account      *Account
	}{
		{"pass opt-in", policyPassAll, apiKey}, // subscription OAuth cannot opt in (#147)
		{"oauth-only block does not cover api key", policyBlockOAuth, apiKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := countOK("1")
			svc := newLongContextService(upstream, tc.policy, nil)
			c, _ := longContextContext("/v1/messages", "")
			_, err := forwardMessages(t, svc, c, tc.account, textMessages("claude-sonnet-4-6", 1024, 12_000_000))
			require.NoError(t, err)
			require.Equal(t, "policy_pass", observationOf(t, c).Decision)
			require.Equal(t, 1, upstream.inferCalls)
			requireNoCount(t, upstream)
		})
	}
}

func TestLongContextAdmission_OutputBudgetAloneRejectsLocally(t *testing.T) {
	upstream := countOK("1")
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	body := `{"model":"claude-sonnet-4-5","max_tokens":191809,"messages":[{"role":"user","content":"hi"}]}`
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
	requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
	require.Equal(t, "output_reject", observationOf(t, c).Decision, "applies to every model, profiled or not")
	require.Zero(t, upstream.countCalls+upstream.inferCalls)
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
			upstream := countOK("1")
			svc := newLongContextService(upstream, policy, nil)
			c, _ := longContextContext("/v1/messages", "context-1m-2025-08-07")
			_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), strings.Replace(smallMessages, "claude-sonnet-4-6", "claude-sonnet-5", 1))
			requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
			obs := observationOf(t, c)
			require.Equal(t, BetaPolicyActionBlock, obs.Action)
			require.Equal(t, "marker_reject", obs.Decision)
			require.Zero(t, upstream.inferCalls)
		})
	}
}

// Content, models and routes without a calibrated bound are admitted (output arithmetic
// and the marker still apply) and recorded, so the observe log can measure the gap.
func TestLongContextAdmission_UnboundedInputsAreAdmittedAndRecorded(t *testing.T) {
	head := `{"model":"claude-sonnet-4-6","max_tokens":1024,`
	huge := strings.Repeat("x", 2_500_000)
	for _, tc := range []struct {
		name, body, eligibility string
	}{
		{"unknown model", textMessages("claude-sonnet-4-5", 1024, 2_500_000), "unprofiled"},
		{"dated alias", textMessages("claude-sonnet-4-6-20260301", 1024, 2_500_000), "unprofiled"},
		{"mcp servers", head + `"mcp_servers":[{"type":"url","url":"https://mcp.example","name":"m"}],"messages":[{"role":"user","content":"` + huge + `"}]}`, "ineligible"},
		{"image", head + `"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + huge + `"}}]}]}`, "ineligible"},
		{"citations", head + `"messages":[{"role":"user","content":[{"type":"text","text":"hi","citations":[]}]}]}`, "ineligible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := countOK("1")
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext("/v1/messages", "")
			_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), tc.body)
			require.NoError(t, err)
			obs := observationOf(t, c)
			require.Equal(t, tc.eligibility, obs.Eligibility)
			require.Equal(t, tc.eligibility+"_admit", obs.Decision)
			require.Equal(t, 1, upstream.inferCalls)
			requireNoCount(t, upstream)
		})
	}
	t.Run("routes without a profile", func(t *testing.T) {
		svc := newLongContextService(countOK("1"), "", nil)
		for _, a := range []*Account{
			{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeServiceAccount},
			{ID: 10, Platform: PlatformAnthropic, Type: AccountTypeBedrock},
			{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		} {
			require.Equal(t, longContextRouteOther, longContextRouteFor(a))
			c, _ := longContextContext("/v1/messages", "")
			require.NoError(t, svc.admitLongContext(context.Background(), c, anthropicLongContextAttempt("test", a, "claude-sonnet-4-6", []byte(textMessages("claude-sonnet-4-6", 1024, 2_500_000)), nil, false)))
			require.Equal(t, "unprofiled_admit", observationOf(t, c).Decision)
		}
	})
}

func TestLongContextAdmission_CompatAdapters(t *testing.T) {
	big := strings.Repeat("calibrated text ", 160_000) // about 2.5 MB of input text
	for _, tc := range []struct {
		name, path string
		call       func(*GatewayService, *gin.Context, *Account, string) (*ForwardResult, error)
	}{
		{"chat completions", "/v1/chat/completions", func(svc *GatewayService, c *gin.Context, a *Account, text string) (*ForwardResult, error) {
			return svc.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"`+text+`"}],"stream":true}`), nil)
		}},
		{"responses", "/v1/responses", func(svc *GatewayService, c *gin.Context, a *Account, text string) (*ForwardResult, error) {
			return svc.ForwardAsResponses(context.Background(), c, a, []byte(`{"model":"claude-sonnet-4-6","input":"`+text+`"}`), nil)
		}},
	} {
		t.Run(tc.name+" oversized", func(t *testing.T) {
			upstream := countOK("1")
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext(tc.path, "")
			result, err := tc.call(svc, c, newAnthropicOAuthAccountForPartialUsageTest(), big)
			require.Nil(t, result)
			requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
			require.Contains(t, []string{"profile_reject", "estimate_reject"}, observationOf(t, c).Decision)
			require.Zero(t, upstream.inferCalls)
			requireNoCount(t, upstream)
		})
		t.Run(tc.name+" normal", func(t *testing.T) {
			upstream := countOK("1")
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext(tc.path, "")
			_, err := tc.call(svc, c, newAnthropicOAuthAccountForPartialUsageTest(), "hello")
			require.NoError(t, err)
			require.Equal(t, 1, upstream.inferCalls)
			requireNoCount(t, upstream)
		})
	}
}

// The budget rectifier retry raises max_tokens to 64000, which shrinks the input budget:
// the rewritten body is decided again and stopped before its inference call.
func TestLongContextAdmission_RewrittenRetryIsDecidedAgain(t *testing.T) {
	upstream := &longContextUpstream{countStatus: http.StatusOK, inferResps: []*http.Response{{
		StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"thinking.budget_tokens: Input should be greater than or equal to 1024"}}`)),
	}}}
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	body := strings.Replace(textMessages("claude-sonnet-4-6", 4096, 2_350_000), `"max_tokens":4096,`, `"max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":512},`, 1)
	_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
	obs := observationOf(t, c)
	requireLongContextStop(t, err, GatewayFailureReasonLongContextDisabled, http.StatusBadRequest)
	require.Equal(t, "estimate_reject", obs.Decision)
	require.Equal(t, "eligible", obs.Eligibility, "thinking plus the context_management mimicry adds stays decidable")
	require.Equal(t, int64(BudgetRectifyMaxTokens), obs.MaxTokens, "the retry's own budget")
	require.Equal(t, 1, upstream.inferCalls, "the rewritten retry never reaches inference")
	requireNoCount(t, upstream)
}

// The observe line carries the decision and, when inference ran, the provider's actual
// input usage, so the estimates can be checked against real traffic afterwards.
func TestLogLongContextOutcome(t *testing.T) {
	sink, restore := captureStructuredLog(t)
	defer restore()

	upstream := countOK("1")
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	result, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), textMessages("claude-sonnet-4-6", 1024, 1_500_000))
	require.NoError(t, err)
	result.Usage = ClaudeUsage{InputTokens: 10, CacheCreationInputTokens: 20, CacheReadInputTokens: 70_000}
	LogLongContextOutcome(c, result, nil)

	c2, _ := longContextContext("/v1/messages", "")
	_, err = forwardMessages(t, svc, c2, newAnthropicOAuthAccountForPartialUsageTest(), textMessages("claude-sonnet-4-6", 1024, 3_000_000))
	LogLongContextOutcome(c2, nil, err)
	LogLongContextOutcome(c2, nil, err) // already logged: nothing more

	var lines []map[string]any
	sink.mu.Lock()
	for _, ev := range sink.events {
		if ev.Message == "long_context.observe" {
			lines = append(lines, ev.Fields)
		}
	}
	sink.mu.Unlock()
	require.Len(t, lines, 2)
	require.Equal(t, "estimate_admit", lines[0]["decision"])
	require.EqualValues(t, 70_030, lines[0]["total_input_tokens"])
	require.Equal(t, false, lines[0]["actual_over_budget"])
	require.Equal(t, "inference", lines[0]["outcome"])
	require.EqualValues(t, 1_000_000, lines[0]["window"])
	require.Equal(t, "pricing", lines[0]["window_source"])
	for _, k := range []string{"body_bytes", "max_tokens", "input_budget", "profile_lower", "profile_upper", "estimate", "mimic", "route", "model", "entry", "account_id", "eligibility", "action"} {
		require.Contains(t, lines[0], k)
	}
	require.Equal(t, "estimate_reject", lines[1]["decision"])
	require.Equal(t, string(GatewayFailureReasonLongContextDisabled), lines[1]["outcome"])
	for _, l := range lines {
		for k := range l {
			require.NotContains(t, k, "content")
			require.NotContains(t, k, "token_value")
		}
	}
	requireNoCount(t, upstream)
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
		`{"thinking":{"type":"enabled","budget_tokens":1024},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"user","content":"x"}]}`: true,
	} {
		require.Equal(t, want, isLongContextLocallyEligible([]byte(body)), body)
	}
}

func TestSetBetaPolicyRejectsDuplicateLongContextRule(t *testing.T) {
	svc := NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{}}}, &config.Config{})
	err := svc.SetBetaPolicySettings(context.Background(), &BetaPolicySettings{Rules: []BetaPolicyRule{
		{BetaToken: longContextBetaToken, Action: "pass", Scope: "all"},
		{BetaToken: longContextBetaToken, Action: "block", Scope: "oauth"},
	}})
	require.ErrorContains(t, err, "duplicate")
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

// The window is the model's own capacity from the pricing data; an unknown model gets no
// numeric gate (only the marker rule applies) and says so in the observe log.
func TestLongContextWindowComesFromModelCapacity(t *testing.T) {
	svc := newLongContextService(countOK("1"), "", nil)
	for model, want := range map[string]int64{
		"claude-sonnet-4-6": 1_000_000, "claude-opus-4-6": 1_000_000, "claude-opus-4-7": 1_000_000, "claude-opus-5-5": 1_000_000,
		"claude-sonnet-4-5": 200_000, "claude-haiku-4-5-20251001": 200_000, "claude-opus-4-1": 200_000,
		"not-a-model-xyz": 0,
	} {
		w, src := svc.longContextWindow(model)
		require.Equal(t, want, w, model)
		require.Equal(t, map[bool]string{true: "pricing", false: "unknown"}[want > 0], src, model)
	}
	require.Equal(t, int64(0), func() int64 { w, _ := (&GatewayService{}).longContextWindow("claude-sonnet-4-6"); return w }(), "no pricing wired: unknown")

	// Scope, not protection: a 1M window without a calibrated profile bounds max_tokens only.
	// A 12 MB unprofiled Opus 5.5 input is admitted under the default block policy.
	for _, tc := range []struct {
		model, decision string
		window          int64
		bytes           int
	}{
		{"claude-sonnet-4-5", "unprofiled_admit", 200_000, 600_000},
		{"claude-opus-5-5", "unprofiled_admit", 1_000_000, 12_000_000},
		{"not-a-model-xyz", "unknown_window_admit", 0, 600_000},
	} {
		upstream := countOK("1")
		svc := newLongContextService(upstream, "", nil)
		c, _ := longContextContext("/v1/messages", "")
		_, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), textMessages(tc.model, 1024, tc.bytes))
		require.NoError(t, err, tc.model)
		obs := observationOf(t, c)
		require.Equal(t, tc.decision, obs.Decision, tc.model)
		require.Equal(t, tc.window, obs.Window, tc.model)
		require.Equal(t, 1, upstream.inferCalls)
		requireNoCount(t, upstream)
	}
}

// An uncalibrated 1M model is not gated by size, so the observe line must show when the
// provider itself rejected the size, plus a recorded-only estimate for later analysis.
func TestLogLongContextOutcomeRecordsUpstreamOverflow(t *testing.T) {
	sink, restore := captureStructuredLog(t)
	defer restore()
	upstream := &longContextUpstream{countStatus: http.StatusOK, inferResps: []*http.Response{{
		StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1200000 tokens > 1000000 maximum"}}`)),
	}}}
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	res, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), textMessages("claude-opus-5-5", 1024, 600_000))
	require.Error(t, err)
	LogLongContextOutcome(c, res, err)

	var line map[string]any
	sink.mu.Lock()
	for _, ev := range sink.events {
		if ev.Message == "long_context.observe" {
			line = ev.Fields
		}
	}
	sink.mu.Unlock()
	require.NotNil(t, line)
	require.Equal(t, "unprofiled_admit", line["decision"], "recording never changes the decision")
	require.EqualValues(t, longContextRealisticEstimate(int64(observationBytes(line))), line["estimate"])
	require.EqualValues(t, 400, line["upstream_status"])
	require.Equal(t, "invalid_request_error", line["upstream_error_type"])
	require.Equal(t, true, line["prompt_too_long"])
	require.Equal(t, 1, upstream.inferCalls)
	requireNoCount(t, upstream)
}

func observationBytes(line map[string]any) int64 {
	switch v := line["body_bytes"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return -1
}

// An internal retry keeps the replaced attempt's outcome: the failed first attempt is written
// as superseded_by_retry with its upstream status, the successful retry carries the usage,
// and the final record is written once (#153 logging G0).
func TestLogLongContextOutcomeKeepsRetriedAttempt(t *testing.T) {
	sink, restore := captureStructuredLog(t)
	defer restore()
	upstream := &longContextUpstream{countStatus: http.StatusOK, inferResps: []*http.Response{{
		StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"thinking.budget_tokens: Input should be greater than or equal to 1024"}}`)),
	}}}
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	body := `{"model":"claude-sonnet-4-5","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"hi"}]}`
	res, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), body)
	require.NoError(t, err)
	require.Equal(t, 2, upstream.inferCalls, "the budget rectifier retried once")
	res.Usage = ClaudeUsage{InputTokens: 12}
	LogLongContextOutcome(c, res, nil)
	LogLongContextOutcome(c, res, nil) // already logged: nothing more

	var lines []map[string]any
	sink.mu.Lock()
	for _, ev := range sink.events {
		if ev.Message == "long_context.observe" {
			lines = append(lines, ev.Fields)
		}
	}
	sink.mu.Unlock()
	require.Len(t, lines, 2)
	require.EqualValues(t, 1, lines[0]["attempt"])
	require.Equal(t, "superseded_by_retry", lines[0]["outcome"])
	require.EqualValues(t, 400, lines[0]["upstream_status"])
	require.NotContains(t, lines[0], "input_tokens", "no usage is invented for the failed attempt")
	require.EqualValues(t, 2, lines[1]["attempt"])
	require.Equal(t, "inference", lines[1]["outcome"])
	require.EqualValues(t, 12, lines[1]["input_tokens"])
	require.NotContains(t, lines[1], "upstream_status")
}

func longContextObserveLines(sink *inMemoryLogSink) []map[string]any {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var lines []map[string]any
	for _, ev := range sink.events {
		if ev.Message == "long_context.observe" {
			lines = append(lines, ev.Fields)
		}
	}
	return lines
}

func jsonError(status int, message string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"` + message + `"}}`))}
}

// A generic retry (non-400) records the replaced attempt's status before retrying (R1).
func TestLogLongContextOutcomeGenericRetryKeepsStatus(t *testing.T) {
	sink, restore := captureStructuredLog(t)
	defer restore()
	upstream := &longContextUpstream{countStatus: http.StatusOK, inferResps: []*http.Response{jsonError(http.StatusForbidden, "forbidden")}}
	svc := newLongContextService(upstream, "", nil)
	c, _ := longContextContext("/v1/messages", "")
	res, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), `{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, err)
	require.Equal(t, 2, upstream.inferCalls, "403 is retried on the same account")
	LogLongContextOutcome(c, res, nil)

	lines := longContextObserveLines(sink)
	require.Len(t, lines, 2)
	require.Equal(t, "superseded_by_retry", lines[0]["outcome"])
	require.EqualValues(t, 403, lines[0]["upstream_status"])
	require.Equal(t, "inference", lines[1]["outcome"])
	require.EqualValues(t, 2, lines[1]["attempt"])
}

// A rewritten-body retry whose transport fails is written as retry_transport_error, with no
// status; the earlier response restored for the client stays on its own line only (R2).
func TestLogLongContextOutcomeRetryTransportFailure(t *testing.T) {
	transport := errors.New("dial tcp: connection reset")
	thinking := `{"model":"claude-sonnet-4-5","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},` +
		`"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"bad"},{"type":"tool_use","id":"t1","name":"x","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`
	for _, tc := range []struct {
		name      string
		resps     []*http.Response
		errs      []error
		wantFirst []int // upstream_status of the HTTP attempts, in order
	}{
		{"budget", []*http.Response{jsonError(400, "thinking.budget_tokens: Input should be greater than or equal to 1024")}, []error{nil, transport}, []int{400}},
		{"signature", []*http.Response{jsonError(400, "Invalid signature in thinking block")}, []error{nil, transport}, []int{400}},
		{"signature tools", []*http.Response{jsonError(400, "Invalid signature in thinking block"), jsonError(400, "Invalid signature in thinking block for tool_use")},
			[]error{nil, nil, transport}, []int{400, 400}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink, restore := captureStructuredLog(t)
			defer restore()
			upstream := &longContextUpstream{countStatus: http.StatusOK, inferResps: tc.resps, inferErrs: tc.errs}
			svc := newLongContextService(upstream, "", nil)
			c, _ := longContextContext("/v1/messages", "")
			res, err := forwardMessages(t, svc, c, newAnthropicOAuthAccountForPartialUsageTest(), thinking)
			require.Error(t, err, "the client still gets the earlier HTTP error")
			require.Equal(t, len(tc.errs), upstream.inferCalls)
			LogLongContextOutcome(c, res, err)

			lines := longContextObserveLines(sink)
			require.Len(t, lines, len(tc.wantFirst)+1, "no duplicate final line")
			for i, status := range tc.wantFirst {
				require.Equal(t, "superseded_by_retry", lines[i]["outcome"], i)
				require.EqualValues(t, status, lines[i]["upstream_status"], i)
				require.EqualValues(t, i+1, lines[i]["attempt"])
			}
			last := lines[len(lines)-1]
			require.Equal(t, "retry_transport_error", last["outcome"])
			require.NotContains(t, last, "upstream_status", "the failed retry received no HTTP response")
			require.NotContains(t, last, "input_tokens")
		})
	}
}
