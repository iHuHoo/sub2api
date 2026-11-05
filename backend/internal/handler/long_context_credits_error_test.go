package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const longContextCode = "long_context_usage_credits_required"

func longContextStop() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:        http.StatusTooManyRequests,
		ResponseBody:      []byte(`{"error":{"message":"Usage credits are required for long context requests."}}`),
		Reason:            service.GatewayFailureReasonLongContextCredits,
		NextAccountAction: service.NextAccountStop,
		ClientStatusCode:  http.StatusBadRequest,
		ClientMessage:     "Long context requests require usage credits on the upstream account.",
	}
}

func TestLongContextCreditsStopEndsFailoverWithoutMarkingAccount(t *testing.T) {
	fs := NewFailoverState(3, false)
	action := fs.HandleFailoverError(context.Background(), nil, 7, service.PlatformAnthropic, 3, longContextStop())
	require.Equal(t, FailoverExhausted, action)
	require.Empty(t, fs.FailedAccountIDs, "the account did not fail; the request did")
	require.Zero(t, fs.SwitchCount)
}

func TestLongContextCreditsExhaustedResponses(t *testing.T) {
	h := &GatewayHandler{}
	writers := map[string]struct {
		path  string
		write func(*gin.Context, bool)
		code  string // JSON path of the code before headers
	}{
		"messages": {"/v1/messages", func(c *gin.Context, s bool) {
			h.handleFailoverExhausted(c, longContextStop(), service.PlatformAnthropic, s)
		}, "error.code"},
		"chat completions": {"/v1/chat/completions", func(c *gin.Context, s bool) { h.handleCCFailoverExhausted(c, longContextStop(), s) }, "error.code"},
		"responses":        {"/v1/responses", func(c *gin.Context, s bool) { h.handleResponsesFailoverExhausted(c, longContextStop(), s) }, "error.code"},
	}
	for name, w := range writers {
		t.Run(name+" before headers", func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, w.path, nil)
			w.write(c, false)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, longContextCode, gjson.Get(rec.Body.String(), w.code).String(), rec.Body.String())
			require.Empty(t, rec.Header().Get("Retry-After"))
		})
		t.Run(name+" after keepalive", func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, w.path, nil)
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			n, err := c.Writer.WriteString(": ping\n\n")
			require.NoError(t, err)
			c.Writer.Flush()
			recordGatewayStreamHeartbeat(c, n) // as the slot-wait keepalive does

			w.write(c, true)

			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code, "a committed status cannot change")
			require.True(t, strings.HasPrefix(body, ": ping\n\n"))
			frame := strings.TrimPrefix(body, ": ping\n\n")
			require.True(t, strings.HasPrefix(frame, "event: ") || strings.HasPrefix(frame, "data: "), "terminal SSE frame, not bare JSON: %q", frame)
			require.True(t, strings.HasSuffix(frame, "\n\n"), frame)
			require.Contains(t, frame, longContextCode)
			if name == "messages" {
				require.True(t, strings.HasPrefix(frame, "event: error\ndata: "), frame)
			}
			if name == "responses" {
				require.True(t, strings.HasPrefix(frame, "event: response.failed\ndata: "), frame)
			}
		})
	}
}

// When the compat stream writer already sent the coded terminal frame, the handler adds nothing.
func TestLongContextCreditsHandlerKeepsSingleTerminalFrame(t *testing.T) {
	h := &GatewayHandler{}
	for name, write := range map[string]func(*gin.Context){
		"messages":         func(c *gin.Context) { h.handleFailoverExhausted(c, longContextStop(), service.PlatformAnthropic, true) },
		"chat completions": func(c *gin.Context) { h.handleCCFailoverExhausted(c, longContextStop(), true) },
		"responses":        func(c *gin.Context) { h.handleResponsesFailoverExhausted(c, longContextStop(), true) },
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/x", nil)
			_, err := c.Writer.WriteString("data: {\"error\":{\"code\":\"" + longContextCode + "\"}}\n\n")
			require.NoError(t, err)
			service.MarkResponseCommitted(c)
			before := rec.Body.String()

			write(c)

			require.Equal(t, before, rec.Body.String())
		})
	}
}

// Local admission stops (#139) use the same writers with their own status, type and code.
func TestLongContextAdmissionStopResponses(t *testing.T) {
	h := &GatewayHandler{}
	stops := []struct {
		reason  service.GatewayFailureReason
		status  int
		errType string
	}{
		{service.GatewayFailureReasonLongContextDisabled, http.StatusBadRequest, "invalid_request_error"},
		{service.GatewayFailureReasonLongContextCountUnavailable, http.StatusServiceUnavailable, "api_error"},
		{service.GatewayFailureReasonLongContextPolicyUnavailable, http.StatusServiceUnavailable, "api_error"},
	}
	for _, st := range stops {
		stop := func() *service.UpstreamFailoverError {
			return &service.UpstreamFailoverError{Reason: st.reason, NextAccountAction: service.NextAccountStop, ClientStatusCode: st.status, ClientMessage: "m"}
		}
		writers := map[string]struct {
			path  string
			write func(*gin.Context, bool)
		}{
			"messages":         {"/v1/messages", func(c *gin.Context, s bool) { h.handleFailoverExhausted(c, stop(), service.PlatformAnthropic, s) }},
			"chat completions": {"/v1/chat/completions", func(c *gin.Context, s bool) { h.handleCCFailoverExhausted(c, stop(), s) }},
			"responses":        {"/v1/responses", func(c *gin.Context, s bool) { h.handleResponsesFailoverExhausted(c, stop(), s) }},
		}
		for name, w := range writers {
			t.Run(string(st.reason)+"/"+name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, w.path, nil)
				w.write(c, false)
				require.Equal(t, st.status, rec.Code, rec.Body.String())
				require.Equal(t, string(st.reason), gjson.Get(rec.Body.String(), "error.code").String(), rec.Body.String())
				if name != "responses" {
					require.Equal(t, st.errType, gjson.Get(rec.Body.String(), "error.type").String())
				}
				require.Empty(t, rec.Header().Get("Retry-After"))

				rec2 := httptest.NewRecorder()
				c2, _ := gin.CreateTestContext(rec2)
				c2.Request = httptest.NewRequest(http.MethodPost, w.path, nil)
				c2.Writer.Header().Set("Content-Type", "text/event-stream")
				n, _ := c2.Writer.WriteString(": ping\n\n")
				c2.Writer.Flush()
				recordGatewayStreamHeartbeat(c2, n)
				w.write(c2, true)
				frame := strings.TrimPrefix(rec2.Body.String(), ": ping\n\n")
				require.Equal(t, 1, strings.Count(frame, string(st.reason)), frame)
				if name != "responses" { // the Responses error object carries only code and message
					require.Contains(t, frame, st.errType)
				}
			})
		}
	}
}
