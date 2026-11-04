//go:build unit

package service

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const longContextCreditsBody = `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."}}`

func TestIsAnthropicLongContextCreditsRequired(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"confirmed message", http.StatusTooManyRequests, longContextCreditsBody, true},
		{"case and trailing period", http.StatusTooManyRequests, `{"error":{"message":"  usage credits are required for long context requests  "}}`, true},
		{"other status", http.StatusBadRequest, longContextCreditsBody, false},
		{"model credits message", http.StatusTooManyRequests, `{"error":{"message":"Usage credits are required for this model."}}`, false},
		{"phrase outside error.message", http.StatusTooManyRequests, `{"error":{"message":"rate limited"},"echo":"Usage credits are required for long context requests."}`, false},
		{"phrase embedded in longer message", http.StatusTooManyRequests, `{"error":{"message":"note: Usage credits are required for long context requests. Retry later"}}`, false},
		{"not json", http.StatusTooManyRequests, `Usage credits are required for long context requests.`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isAnthropicLongContextCreditsRequired(tc.status, []byte(tc.body)))
		})
	}
}

func TestHandleUpstreamError_LongContextCreditsWritesNoAccountState(t *testing.T) {
	// The aggregated reset can be far away; it must not become an account cooldown.
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(time.Now().Add(26*24*time.Hour).Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-7d-status", "allowed")

	repo := &anthropicWindowLimitRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 39, Type: AccountTypeOAuth, Platform: PlatformAnthropic, Credentials: map[string]any{
		"temp_unschedulable_enabled": true,
		"temp_unschedulable_rules": []any{map[string]any{
			"error_code": float64(429), "keywords": []any{"credits"}, "duration_minutes": float64(60),
		}},
	}}

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(longContextCreditsBody), "claude-sonnet-4-6")

	require.False(t, shouldDisable)
	require.Zero(t, repo.rateLimitCalls, "long context entitlement must not rate limit the account")
	require.Zero(t, repo.sessionWindowCalls, "long context entitlement must not rewrite the session window")
	require.Zero(t, repo.modelRateLimitCalls, "long context entitlement is request scoped, not model scoped")
	require.Zero(t, repo.tempUnschedCalls, "a broad custom 429 rule must not pause the account for this reason")
}

func TestHandleUpstreamError_LongContextCreditsKeepsExhaustedSharedWindow(t *testing.T) {
	reset5h := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(reset5h.Unix(), 10))

	repo := &anthropicWindowLimitRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 39, Type: AccountTypeOAuth, Platform: PlatformAnthropic}

	svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(longContextCreditsBody), "claude-sonnet-4-6")

	require.Equal(t, 1, repo.rateLimitCalls, "a genuinely exhausted 5h window must still be recorded")
	require.Equal(t, reset5h, repo.lastRateLimitReset)
}
