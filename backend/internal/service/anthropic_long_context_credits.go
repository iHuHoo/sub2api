package service

import (
	"bytes"
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
	anthropicLongContextCreditsMessage    = "usage credits are required for long context requests"
	anthropicLongContextCreditsCode       = "long_context_usage_credits_required"
	anthropicLongContextCreditsClientText = "Long context requests require usage credits on the upstream account. Disable long context (1M) or ask the account owner to enable usage credits."
)

// isAnthropicLongContextCreditsRequired matches only the whole upstream error.message,
// so echoed user content or a longer, different message never matches.
func isAnthropicLongContextCreditsRequired(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	msg := strings.TrimSuffix(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()), ".")
	return strings.EqualFold(strings.TrimSpace(msg), anthropicLongContextCreditsMessage)
}

// writeAnthropicLongContextCreditsError answers with a non-retryable 400 and no
// Retry-After, so SDKs do not resend the same request.
func writeAnthropicLongContextCreditsError(c *gin.Context) {
	MarkResponseCommitted(c)
	c.JSON(http.StatusBadRequest, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "invalid_request_error",
			"code":    anthropicLongContextCreditsCode,
			"message": anthropicLongContextCreditsClientText,
		},
	})
}

// writeGatewayCCLongContextCreditsError is the Chat Completions form of the same answer.
func writeGatewayCCLongContextCreditsError(c *gin.Context) {
	MarkResponseCommitted(c)
	c.JSON(http.StatusBadRequest, gin.H{
		"error": gin.H{
			"type":    "invalid_request_error",
			"code":    anthropicLongContextCreditsCode,
			"message": anthropicLongContextCreditsClientText,
		},
	})
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
