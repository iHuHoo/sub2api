//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The failover-exhausted fallback shares service.AnthropicClientError. Its callers include the
// Anthropic, Gemini /messages and SystemOne failover paths, so the shared effects are pinned
// here: 413 keeps its meaning, an unmapped status is named, and credential failures stay 502.
func TestFailoverExhaustedUsesSharedClientMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for upstream, want := range map[int]struct {
		status       int
		errType, msg string
	}{
		413: {413, "request_too_large", "Request is too large for the upstream API; shorten the conversation or remove large attachments"},
		404: {502, "upstream_error", "Upstream request failed (upstream status 404)"},
		401: {502, "upstream_error", "Upstream authentication failed, please contact administrator"},
		529: {503, "overloaded_error", "Upstream service overloaded, please retry later"},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		(&GatewayHandler{}).handleFailoverExhaustedSimple(c, upstream, false)
		require.Equal(t, want.status, rec.Code, upstream)
		require.Equal(t, want.errType, gjson.GetBytes(rec.Body.Bytes(), "error.type").String(), upstream)
		require.Equal(t, want.msg, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), upstream)
	}
}
