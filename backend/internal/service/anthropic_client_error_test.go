//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The client-facing contract for Anthropic upstream errors (messages and count_tokens): account
// and credential failures never surface as the client's own 401/403, a too-large request keeps
// its meaning, and the generic fallback names the upstream status.
func TestAnthropicClientError(t *testing.T) {
	for upstream, want := range map[int]struct {
		status       int
		errType, msg string
	}{
		401: {502, "upstream_error", "Upstream authentication failed, please contact administrator"},
		403: {502, "upstream_error", "Upstream access forbidden, please contact administrator"},
		413: {413, "request_too_large", "Request is too large for the upstream API; shorten the conversation or remove large attachments"},
		429: {429, "rate_limit_error", "Upstream rate limit exceeded, please retry later"},
		529: {503, "overloaded_error", "Upstream service overloaded, please retry later"},
		503: {502, "upstream_error", "Upstream service temporarily unavailable"},
		404: {502, "upstream_error", "Upstream request failed (upstream status 404)"},
	} {
		status, errType, msg := AnthropicClientError(upstream)
		assert.Equal(t, want.status, status, upstream)
		assert.Equal(t, want.errType, errType, upstream)
		assert.Equal(t, want.msg, msg, upstream)
	}
}

// count_tokens follows the same contract on the native and API-key passthrough paths: an
// upstream 401 is not the client's 401, and an upstream 400 keeps status 400 with a bounded,
// sanitized message in a valid JSON envelope; raw bodies never reach the client.
func TestForwardCountTokensClientErrors(t *testing.T) {
	const secret = "review-secret-marker"
	for _, tc := range []struct {
		name          string
		upstream      int
		body          string
		want          int
		wantMsg       string
		nativeSkipped bool // native 401 marks the account, which needs a full rate-limit service
	}{
		{"structured 400", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"}}`, 400, "messages: at least one message is required", false},
		{"html 400", 400, `<html>proxy rejected request</html>`, 400, countTokensBadRequestFallback, false},
		{"empty 400", 400, ``, 400, countTokensBadRequestFallback, false},
		{"secret url 400", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"see https://example.invalid/?access_token=` + secret + `"}}`, 400, "see https://example.invalid/", false},
		{"long 400", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"` + strings.Repeat("x", 5000) + `"}}`, 400, strings.Repeat("x", countTokensMaxClientMessageBytes), false},
		{"object message 400", 400, `{"error":{"message":{"access_token":"` + secret + `"}}}`, 400, countTokensBadRequestFallback, false},
		{"object detail 400", 400, `{"detail":{"access_token":"` + secret + `"}}`, 400, countTokensBadRequestFallback, false},
		{"array message 400", 400, `{"error":{"message":["` + secret + `"]}}`, 400, countTokensBadRequestFallback, false},
		{"number message 400", 400, `{"error":{"message":42}}`, 400, countTokensBadRequestFallback, false},
		{"malformed json 400", 400, `{"error":{"message":"` + secret + `"`, 400, countTokensBadRequestFallback, false},
		{"string detail 400", 400, `{"detail":"bad field: messages"}`, 400, "bad field: messages", false},
		{"nested message 400", 400, `{"error":{"message":"{\"error\":{\"message\":\"inner reason\"}}"}}`, 400, "inner reason", false},
		{"nested invalid 400", 400, `{"error":{"message":"{\"access_token\":\"` + secret + `\""}}`, 400, countTokensBadRequestFallback, false},
		{"nested object 400", 400, `{"error":{"message":"{\"error\":{\"message\":{\"access_token\":\"` + secret + `\"}}}"}}`, 400, countTokensBadRequestFallback, false},
		{"413", 413, `{"type":"error","error":{"type":"request_too_large","message":"too large"}}`, 413, "Request is too large for the upstream API", false},
		{"401", 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, 502, "Upstream authentication failed, please contact administrator", true},
	} {
		for _, passthrough := range []bool{false, true} {
			if tc.nativeSkipped && !passthrough {
				continue
			}
			t.Run(fmt.Sprintf("%s/passthrough=%v", tc.name, passthrough), func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
				body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: tc.upstream,
					Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body))}}
				cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
				svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: &RateLimitService{},
					settingService: NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub{values: map[string]string{}}, nil}, cfg)}
				account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
					Credentials: map[string]any{"api_key": "sk-x", "base_url": "https://api.anthropic.com"}, Status: StatusActive, Schedulable: true}
				if passthrough {
					account.Extra = map[string]any{"anthropic_passthrough": true}
					svc.rateLimitService = nil // the passthrough path skips account marking when unset
				}
				err := svc.ForwardCountTokens(context.Background(), c, account, &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-6"})
				require.Error(t, err)
				require.Equal(t, tc.want, rec.Code)
				require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
				require.Equal(t, "error", gjson.GetBytes(rec.Body.Bytes(), "type").String())
				msg := gjson.GetBytes(rec.Body.Bytes(), "error.message").String()
				require.Contains(t, msg, tc.wantMsg)
				require.LessOrEqual(t, len(msg), countTokensMaxClientMessageBytes)
				require.NotContains(t, rec.Body.String(), secret)
				require.NotContains(t, rec.Body.String(), "<html>")
				if tc.want == 400 {
					require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
				}
			})
		}
	}
}
