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
// attempt, no account switch, no account state write, and a non-retryable client 400.
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
		codeAt  string
	}{
		{"messages oauth", "/v1/messages", oauth, forward, "error.code"},
		{"messages api key (same-account retry path)", "/v1/messages", apiKey, forward, "error.code"},
		{"messages api key passthrough", "/v1/messages", newAnthropicAPIKeyAccountForTest(), forward, "error.code"},
		{"chat completions", "/v1/chat/completions", oauth, func(svc *GatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return svc.ForwardAsChatCompletions(context.Background(), c, a, messages, nil)
		}, "error.code"},
		{"responses", "/v1/responses", oauth, func(svc *GatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return svc.ForwardAsResponses(context.Background(), c, a, []byte(`{"model":"claude-sonnet-4-6","input":"hello"}`), nil)
		}, "error.code"},
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
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "must not switch accounts")
			require.Equal(t, 1, upstream.callCount, "exactly one upstream attempt")
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Empty(t, rec.Header().Get("Retry-After"))
			require.Equal(t, anthropicLongContextCreditsCode, gjson.Get(rec.Body.String(), tt.codeAt).String(), rec.Body.String())
			require.Zero(t, repo.rateLimitCalls)
			require.Zero(t, repo.sessionWindowCalls)
			require.Zero(t, repo.modelRateLimitCalls)
			require.Zero(t, repo.tempUnschedCalls)
		})
	}
}
