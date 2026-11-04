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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const sseMessageStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-6\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"

// Upstream HTTP 200 with the long context credits error inside the SSE body, through the
// whole Chat Completions / Responses adapters, buffered and streamed to the client.
func TestCompatAdaptersLongContextCreditsSSE(t *testing.T) {
	adapters := map[string]struct {
		path string
		call func(*GatewayService, *gin.Context, *Account, bool) (*ForwardResult, error)
	}{
		"chat completions": {"/v1/chat/completions", func(s *GatewayService, c *gin.Context, a *Account, stream bool) (*ForwardResult, error) {
			return s.ForwardAsChatCompletions(context.Background(), c, a, []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"stream":`+strconv.FormatBool(stream)+`}`), nil)
		}},
		"responses": {"/v1/responses", func(s *GatewayService, c *gin.Context, a *Account, stream bool) (*ForwardResult, error) {
			return s.ForwardAsResponses(context.Background(), c, a, []byte(`{"model":"claude-sonnet-4-6","input":"hello","stream":`+strconv.FormatBool(stream)+`}`), nil)
		}},
	}
	for name, ad := range adapters {
		for _, clientStream := range []bool{false, true} {
			for _, prefix := range []string{"", sseMessageStart} {
				label := name + " stream=" + strconv.FormatBool(clientStream) + " after_message_start=" + strconv.FormatBool(prefix != "")
				t.Run(label, func(t *testing.T) {
					upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: http.StatusOK,
						Header: http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:   io.NopCloser(strings.NewReader(prefix + "event: error\ndata: " + longContextCreditsBody + "\n\n"))}}}
					repo := &anthropicWindowLimitRepo{}
					cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
					svc := &GatewayService{cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg), httpUpstream: upstream,
						rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil), tlsFPProfileService: &TLSFingerprintProfileService{}, deferredService: &DeferredService{}}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, ad.path, nil)

					result, err := ad.call(svc, c, newAnthropicOAuthAccountForPartialUsageTest(), clientStream)

					require.Nil(t, result, "no partial result is billed")
					var stop *UpstreamFailoverError
					require.True(t, errors.As(err, &stop), "want stop, got %v (status=%d body=%s)", err, rec.Code, rec.Body.String())
					require.False(t, stop.ShouldRetryNextAccount())
					require.True(t, stop.IsLongContextCreditsRequired())
					require.Equal(t, http.StatusOK, stop.StatusCode, "the real upstream status was 200")
					require.Equal(t, 1, upstream.callCount)
					require.Zero(t, repo.rateLimitCalls+repo.sessionWindowCalls+repo.modelRateLimitCalls+repo.tempUnschedCalls)

					body := rec.Body.String()
					if !clientStream || prefix == "" {
						require.Empty(t, body, "nothing was sent yet; the handler answers 400")
						require.False(t, IsResponseCommitted(c))
						return
					}
					// Output already started: exactly one terminal error, already carrying the code.
					require.True(t, IsResponseCommitted(c))
					require.Equal(t, 1, strings.Count(body, anthropicLongContextCreditsCode), body)
					require.NotContains(t, body, "rate_limit_error", body)
					if name == "responses" {
						require.Equal(t, 1, strings.Count(body, "event: response.failed\n"), body)
						created := gjson.Get(sseDataAfter(body, "event: response.created\n"), "response.id").String()
						failed := sseDataAfter(body, "event: response.failed\n")
						require.NotEmpty(t, created)
						require.Equal(t, created, gjson.Get(failed, "response.id").String(), "same response id")
						require.Equal(t, anthropicLongContextCreditsCode, gjson.Get(failed, "response.error.code").String())
						require.Greater(t, gjson.Get(failed, "sequence_number").Int(), gjson.Get(sseDataAfter(body, "event: response.created\n"), "sequence_number").Int())
					}
				})
			}
		}
	}
}

// sseDataAfter returns the data payload of the first frame that starts with marker.
func sseDataAfter(body, marker string) string {
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	rest = strings.TrimPrefix(rest, "data: ")
	if j := strings.Index(rest, "\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}
