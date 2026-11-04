//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func longContextCredits429() *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Retry-After", "30")
	h.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(time.Now().Add(26*24*time.Hour).Unix(), 10))
	return &http.Response{StatusCode: http.StatusTooManyRequests, Header: h, Body: io.NopCloser(strings.NewReader(longContextCreditsBody))}
}

// Every Anthropic ingress must stop on the first long context credits 429: one upstream
// attempt, a stop (not switch) instruction carrying the client 400, and no account state
// write. The handler writes the client response so it can respect a started stream.
func TestGatewayLongContextCreditsStopsWithoutFailover(t *testing.T) {
	oauth := newAnthropicOAuthAccountForPartialUsageTest()
	apiKey := &Account{ID: 202, Name: "anthropic-apikey", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "k", "base_url": "https://api.anthropic.com"}, Status: StatusActive, Schedulable: true}
	messages := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`)
	forward := func(svc *GatewayService, c *gin.Context, account *Account) (*ForwardResult, error) {
		parsed, err := ParseGatewayRequest(NewRequestBodyRef(messages), PlatformAnthropic)
		require.NoError(t, err)
		return svc.Forward(context.Background(), c, account, parsed)
	}

	tests := []struct {
		name    string
		path    string
		account *Account
		call    func(*GatewayService, *gin.Context, *Account) (*ForwardResult, error)
	}{
		{"messages oauth", "/v1/messages", oauth, forward},
		{"messages api key (same-account retry path)", "/v1/messages", apiKey, forward},
		{"messages api key passthrough", "/v1/messages", newAnthropicAPIKeyAccountForTest(), forward},
		{"chat completions", "/v1/chat/completions", oauth, func(svc *GatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return svc.ForwardAsChatCompletions(context.Background(), c, a, messages, nil)
		}},
		{"responses", "/v1/responses", oauth, func(svc *GatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return svc.ForwardAsResponses(context.Background(), c, a, []byte(`{"model":"claude-sonnet-4-6","input":"hello"}`), nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{longContextCredits429(), longContextCredits429(), longContextCredits429()}}
			repo := &anthropicWindowLimitRepo{}
			cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
			svc := &GatewayService{
				cfg:                  cfg,
				responseHeaderFilter: compileResponseHeaderFilter(cfg),
				httpUpstream:         upstream,
				rateLimitService:     NewRateLimitService(repo, nil, nil, nil, nil),
				deferredService:      &DeferredService{},
				tlsFPProfileService:  &TLSFingerprintProfileService{},
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, nil)

			result, err := tt.call(svc, c, tt.account)

			require.Nil(t, result)
			var stop *UpstreamFailoverError
			require.True(t, errors.As(err, &stop), "want a stop instruction, got %v", err)
			require.False(t, stop.ShouldRetryNextAccount(), "must not switch accounts")
			require.True(t, stop.IsLongContextCreditsRequired())
			require.Equal(t, http.StatusTooManyRequests, stop.StatusCode, "keeps the real upstream status")
			require.Equal(t, http.StatusBadRequest, stop.ClientStatusCode)
			require.Empty(t, stop.ResponseHeaders, "no upstream Retry-After may reach the client")
			require.False(t, stop.RetryableOnSameAccount)
			require.Equal(t, 1, upstream.callCount, "exactly one upstream attempt")
			require.Zero(t, rec.Body.Len(), "service leaves the client response to the handler")
			require.False(t, IsResponseCommitted(c))
			require.Zero(t, repo.rateLimitCalls)
			require.Zero(t, repo.sessionWindowCalls)
			require.Zero(t, repo.modelRateLimitCalls)
			require.Zero(t, repo.tempUnschedCalls)
		})
	}
}

// count_tokens never streams or fails over, so the service answers directly.
func TestCountTokensLongContextCreditsAnswers400(t *testing.T) {
	for _, account := range []*Account{newAnthropicOAuthAccountForPartialUsageTest(), newAnthropicAPIKeyAccountForTest()} {
		t.Run(account.Name, func(t *testing.T) {
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{longContextCredits429()}}
			repo := &anthropicWindowLimitRepo{}
			cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
			svc := &GatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg), httpUpstream: upstream,
				rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil), tlsFPProfileService: &TLSFingerprintProfileService{}, deferredService: &DeferredService{}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
			require.NoError(t, err)

			require.Error(t, svc.ForwardCountTokens(context.Background(), c, account, parsed))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Equal(t, anthropicLongContextCreditsCode, gjson.Get(rec.Body.String(), "error.code").String())
			require.Empty(t, rec.Header().Get("Retry-After"))
			require.Equal(t, 1, upstream.callCount)
			require.Zero(t, repo.rateLimitCalls+repo.sessionWindowCalls+repo.modelRateLimitCalls+repo.tempUnschedCalls)
		})
	}
}

func TestSSELongContextCreditsErrorStopsSwitching(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	svc := &GatewayService{cfg: &config.Config{}}

	for _, outputStarted := range []bool{false, true} {
		stop := svc.sseStreamErrorEventFailover(context.Background(), c, newAnthropicOAuthAccountForPartialUsageTest(),
			&http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."}}`, "claude-sonnet-4-6", outputStarted)
		require.False(t, stop.ShouldRetryNextAccount())
		require.True(t, stop.IsLongContextCreditsRequired())
		require.Equal(t, http.StatusOK, stop.StatusCode, "no 429 was observed on the wire")
	}

	other := svc.sseStreamErrorEventFailover(context.Background(), c, newAnthropicOAuthAccountForPartialUsageTest(),
		&http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, `{"type":"error","error":{"type":"api_error","message":"boom"}}`, "claude-sonnet-4-6", false)
	require.True(t, other.ShouldRetryNextAccount(), "unrelated stream errors keep switching")
}

// After the stop, the same account still serves an ordinary request.
func TestLongContextCreditsLeavesAccountUsable(t *testing.T) {
	ok := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))}
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{longContextCredits429(), ok}}
	repo := &anthropicWindowLimitRepo{}
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg), httpUpstream: upstream,
		rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil), deferredService: &DeferredService{}, tlsFPProfileService: &TLSFingerprintProfileService{}}
	account := newAnthropicOAuthAccountForPartialUsageTest()
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`)

	for i, wantErr := range []bool{true, false} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
		require.NoError(t, err)
		_, err = svc.Forward(context.Background(), c, account, parsed)
		require.Equal(t, wantErr, err != nil, "request %d: %v", i, err)
		require.True(t, account.IsSchedulable(), "request %d", i)
	}
	require.Equal(t, 2, upstream.callCount)
	require.Zero(t, repo.rateLimitCalls+repo.sessionWindowCalls+repo.modelRateLimitCalls+repo.tempUnschedCalls)
}
