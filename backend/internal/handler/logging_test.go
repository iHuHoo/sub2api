//go:build unit

package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// A local rejection is one Info line with a fixed reason and no error text; any other
// forward error, including a wrapped upstream one, keeps the Error event (#153 logging G2).
func TestLogForwardErrorSeparatesLocalRejects(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	log := zap.New(core)

	local := service.NewLocalRejectError(service.LocalRejectNoCreditFastMode, errors.New("fast mode requires usage credits"))
	logForwardError(log, "gateway.forward_failed", fmt.Errorf("forward: %w", local), zap.Int64("account_id", 7), zap.Error(local))
	entries := logs.TakeAll()
	require.Len(t, entries, 1)
	require.Equal(t, zapcore.InfoLevel, entries[0].Level)
	require.Equal(t, "gateway.local_reject", entries[0].Message)
	got := entries[0].ContextMap()
	require.Equal(t, "no_credit_fast_mode", got["reason"])
	require.Equal(t, "local_reject", got["outcome"])
	require.Equal(t, "gateway.forward_failed", got["event"])
	require.EqualValues(t, 7, got["account_id"])
	require.NotContains(t, got, "error", "the error text stays out of the local-reject line")

	upstream := errors.New("upstream 529")
	logForwardError(log, "gateway.forward_failed", upstream, zap.Error(upstream))
	entries = logs.TakeAll()
	require.Len(t, entries, 1)
	require.Equal(t, zapcore.ErrorLevel, entries[0].Level)
	require.Equal(t, "gateway.forward_failed", entries[0].Message)
}

// The OpenAI chat-completions route over a native Anthropic account: a Claude 5.5 local 400
// is one Info gateway.local_reject with its fixed reason, no Warn/Error and no upstream call;
// a non-local failure on the same route keeps its Warn (#153 logging G2).
func TestOpenAIChatCompletionsLogsLocalRejectOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := service.Account{ID: 1, Name: "native-anthropic", Platform: service.PlatformOpenCodeGo, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "k", "api_protocol": service.APIProtocolAnthropic}}
	for _, tc := range []struct {
		name, body string
		wantLevel  zapcore.Level
		wantMsg    string
		wantCalls  int
	}{
		{"local reject", `{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"hi"}]}`,
			zapcore.InfoLevel, "gateway.local_reject", 0},
		{"upstream 400", `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`,
			zapcore.WarnLevel, "openai_chat_completions.forward_failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &badRequestUpstream{}
			h := newOpenAIFailoverTestHandlerWithAccounts(t, upstream, []service.Account{account})
			h.maxAccountSwitches = 0
			core, logs := observer.New(zap.InfoLevel)
			groupID := int64(3131)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			req = req.WithContext(logger.IntoContext(req.Context(), zap.New(core)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID,
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenCodeGo}, User: &service.User{ID: 100}})
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100})

			h.ChatCompletions(c)

			require.Len(t, upstream.calls(), tc.wantCalls)
			var hits []observer.LoggedEntry
			for _, e := range logs.All() {
				if e.Level >= zapcore.WarnLevel || e.Message == "gateway.local_reject" {
					hits = append(hits, e)
				}
			}
			var names []string
			for _, e := range hits {
				names = append(names, e.Level.String()+" "+e.Message)
			}
			require.Contains(t, names, tc.wantLevel.String()+" "+tc.wantMsg, names)
			if tc.wantMsg == "gateway.local_reject" {
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Len(t, hits, 1, "no duplicate warning or error: %v", names)
				require.Equal(t, service.LocalRejectClaude55Invalid, hits[0].ContextMap()["reason"])
			}
		})
	}
}

// badRequestUpstream answers every call with a non-failover upstream 400.
type badRequestUpstream struct {
	service.HTTPUpstream
	mu sync.Mutex
	n  int
}

func (u *badRequestUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.mu.Lock()
	u.n++
	u.mu.Unlock()
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))}, nil
}

func (u *badRequestUpstream) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, a, c)
}

func (u *badRequestUpstream) calls() []int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return make([]int, u.n)
}
