package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Anthropic rejects a long context request with HTTP 429 when the account has no
// usage credits for it. That is a property of this request, not account health:
// other accounts usually lack the same credits, and normal requests still work.
const (
	anthropicLongContextCreditsMessage = "usage credits are required for long context requests"
	// GatewayFailureReasonLongContextCredits doubles as the stable client error code.
	GatewayFailureReasonLongContextCredits GatewayFailureReason = "long_context_usage_credits_required"
	anthropicLongContextCreditsCode                             = string(GatewayFailureReasonLongContextCredits)
	anthropicLongContextCreditsClientText                       = "Long context requests require usage credits on the upstream account. Disable long context (1M) or ask the account owner to enable usage credits."
)

// isAnthropicLongContextCreditsMessage matches only the whole upstream error.message,
// so echoed user content or a longer, different message never matches.
func isAnthropicLongContextCreditsMessage(body []byte) bool {
	msg := strings.TrimSuffix(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()), ".")
	return strings.EqualFold(strings.TrimSpace(msg), anthropicLongContextCreditsMessage)
}

// isAnthropicLongContextCreditsRequired is the HTTP form: Anthropic sends it as 429.
func isAnthropicLongContextCreditsRequired(statusCode int, body []byte) bool {
	return statusCode == http.StatusTooManyRequests && isAnthropicLongContextCreditsMessage(body)
}

// newLongContextCreditsStop tells the handler to stop account switching and to answer
// 400 in the client's protocol (JSON, or a terminal SSE event once the stream started).
// statusCode is what upstream actually sent; ResponseHeaders stay empty so no
// Retry-After is copied to the client.
func newLongContextCreditsStop(statusCode int, body []byte) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:        statusCode,
		ResponseBody:      body,
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            GatewayFailureReasonLongContextCredits,
		NextAccountAction: NextAccountStop,
		ClientStatusCode:  http.StatusBadRequest,
		ClientMessage:     anthropicLongContextCreditsClientText,
	}
}

func (e *UpstreamFailoverError) IsLongContextCreditsRequired() bool {
	return e != nil && e.Reason == GatewayFailureReasonLongContextCredits
}

// writeAnthropicLongContextCreditsError is for count_tokens, which does not stream; if
// something was already written it still ends with an SSE error event, never bare JSON.
func writeAnthropicLongContextCreditsError(c *gin.Context) {
	MarkResponseCommitted(c)
	payload := gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "invalid_request_error",
			"code":    anthropicLongContextCreditsCode,
			"message": anthropicLongContextCreditsClientText,
		},
	}
	if !c.Writer.Written() {
		c.JSON(http.StatusBadRequest, payload)
		return
	}
	data, _ := json.Marshal(payload)
	_, _ = io.WriteString(c.Writer, "event: error\ndata: "+string(data)+"\n\n")
	c.Writer.Flush()
}

// isLongContextCreditsResponse peeks the error body of an Anthropic 429 and restores it.
func (s *GatewayService) isLongContextCreditsResponse(account *Account, resp *http.Response) bool {
	if account == nil || account.Platform != PlatformAnthropic || resp == nil || resp.Body == nil || resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	body, _ := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return isAnthropicLongContextCreditsRequired(resp.StatusCode, body)
}
