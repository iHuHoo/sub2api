package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type recordingUpstream struct{ calls int }

func (u *recordingUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func (u *recordingUpstream) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, a, c)
}

func TestRedactHeaderValueForLog(t *testing.T) {
	oauth := "sk-ant-oat01-" + strings.Repeat("A", 60) + "TAIL1234"
	got := redactHeaderValueForLog("Authorization", "Bearer "+oauth)
	require.True(t, strings.HasPrefix(got, "Bearer …TAIL1234 (len=81 sha256="), got)
	require.NotContains(t, got, oauth[:20])
	require.Contains(t, redactHeaderValueForLog("X-Api-Key", "sk-ant-api03-"+strings.Repeat("b", 40)), "…bbbbbbbb (len=53")
	require.Contains(t, redactHeaderValueForLog("x-api-key", "sk-"+strings.Repeat("9", 40)), "…99999999", "gateway sk- keys")

	for name, tc := range map[string]struct{ header, value, want string }{
		"short key":              {"x-api-key", "short1", "[redacted len=6]"},
		"password with spaces":   {"X-Password", "mysecret rest", "[redacted len=13]"},
		"long ordinary password": {"X-Password", "correct horse battery staple 2026!", "[redacted len=34]"},
		"long non-token secret":  {"X-Custom-Token", strings.Repeat("z", 40), "[redacted len=40]"},
		"basic auth":             {"Authorization", "Basic dXNlcjpwYXNzd29yZA==", "Basic [redacted len=20]"},
		"proxy basic":            {"Proxy-Authorization", "Basic dXNlcjpw", "Basic [redacted len=8]"},
		"unknown scheme":         {"Authorization", "Digest username=alice, response=abc", "[redacted len=35]"},
		"bearer non-token":       {"Authorization", "Bearer " + strings.Repeat("x", 40), "Bearer [redacted len=40]"},
		"raw non-token auth":     {"Authorization", "plainpassword", "[redacted len=13]"},
		"set-cookie":             {"Set-Cookie", "sid=abc; Path=/; HttpOnly", "[redacted len=25]"},
		"malformed cookie":       {"Cookie", "notacookie", "[redacted cookie len=10]"},
	} {
		require.Equal(t, tc.want, redactHeaderValueForLog(tc.header, tc.value), name)
	}
	cookie := redactHeaderValueForLog("Cookie", "sid="+strings.Repeat("s", 40)+"; theme=dark; key=sk-"+strings.Repeat("c", 30))
	require.Equal(t, "sid=[redacted len=40]; theme=[redacted len=4]; key=…cccccccc (len=33 sha256="+cookie[len(cookie)-13:], cookie)
	require.NotContains(t, cookie, "dark")
	require.Equal(t, "claude-cli/2.1.318 (external, cli)", redactHeaderValueForLog("User-Agent", "claude-cli/2.1.318 (external, cli)"))
	require.Equal(t, "3f1d", redactHeaderValueForLog("X-Claude-Code-Session-Id", "3f1d"), "session ids are not credentials")
	// The legacy debug dump shares the rule.
	require.Equal(t, redactHeaderValueForLog("Cookie", "a=b"), safeHeaderValueForLog("cookie", "a=b"))
}

func claudeHeaderEvents(sink *inMemoryLogSink) []map[string]any {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []map[string]any
	for _, ev := range sink.events {
		if ev != nil && ev.Message == claudeHeaderLogMessage {
			out = append(out, ev.Fields)
		}
	}
	return out
}

// Inbound once, every outbound attempt numbered, both joined by client_request_id;
// background calls get an operation id; untagged requests are not logged.
func TestClaudeHeaderLogPairsInboundAndOutbound(t *testing.T) {
	sink, restore := captureStructuredLog(t)
	defer restore()
	secret := "sk-ant-oat01-" + strings.Repeat("Q", 70)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), logger.L().With(zap.String("client_request_id", "cr-1"))))
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.400 (external, cli)")
	c.Request.Header.Set("X-Api-Key", "sk-gateway-"+strings.Repeat("k", 30))
	c.Request.Header.Set("X-Password", "mysecret rest of the password")
	c.Request.Header.Set("Proxy-Authorization", "Basic dXNlcjpzZWNyZXQ=")
	c.Request.Header.Set("Cookie", "sid=opaque-session-value-1234567890; theme=dark")
	inner := &recordingUpstream{}
	up := WithClaudeHeaderLog(inner)
	account := &Account{ID: 42, Platform: PlatformAnthropic}
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Add("anthropic-beta", "a")
		req.Header.Add("anthropic-beta", "b")
		_, err := up.Do(markClaudeOutbound(c, req, account, "messages"), "", 42, 1)
		require.NoError(t, err)
	}
	plain, _ := http.NewRequest(http.MethodPost, "https://example.com/x", nil)
	_, _ = up.Do(plain, "", 1, 1)
	bg, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	_, _ = up.Do(markClaudeOutbound(nil, bg, account, "models"), "", 42, 1)
	require.Equal(t, 4, inner.calls, "logging never blocks a send")

	evs := claudeHeaderEvents(sink)
	require.Len(t, evs, 4, "1 inbound + 2 outbound + 1 background; the untagged request is silent")
	require.Equal(t, "inbound", evs[0]["direction"])
	for i, ev := range evs[:3] {
		require.Equal(t, "cr-1", ev["client_request_id"], i)
	}
	require.EqualValues(t, 1, evs[1]["attempt"])
	require.EqualValues(t, 2, evs[2]["attempt"])
	require.EqualValues(t, 42, evs[1]["account_id"])
	require.Equal(t, "prepared", evs[1]["snapshot"])
	require.Equal(t, "models", evs[3]["operation"])
	require.NotEmpty(t, evs[3]["operation_id"])
	_, hasInbound := evs[3]["client_request_id"]
	require.False(t, hasInbound, "background calls have no client request")

	all := ""
	for _, ev := range evs {
		hs, ok := ev["headers"].([]any)
		require.True(t, ok)
		for _, h := range hs {
			line, ok := h.(string)
			require.True(t, ok)
			all += line + "\n"
		}
	}
	require.Contains(t, all, "Anthropic-Beta: a\n")
	require.Contains(t, all, "Anthropic-Beta: b\n", "multi-valued headers keep every value")
	require.Contains(t, all, "User-Agent: claude-cli/2.1.400 (external, cli)")
	require.NotContains(t, all, secret)
	require.NotContains(t, all, strings.Repeat("k", 30))
	for _, leaked := range []string{"mysecret", "password", "dXNlcjpzZWNyZXQ=", "opaque-session", "dark"} {
		require.NotContains(t, all, leaked)
	}
	require.Contains(t, all, "X-Password: [redacted len=29]")

	SetClaudeHeaderLogEnabled(false)
	defer SetClaudeHeaderLogEnabled(true)
	off, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	_, _ = up.Do(markClaudeOutbound(nil, off, account, "messages"), "", 42, 1)
	require.Len(t, claudeHeaderEvents(sink), 4, "the switch turns the events off")
}

func TestClaudeHeaderSnapshotIsBounded(t *testing.T) {
	h := http.Header{}
	h.Set("X-Big", strings.Repeat("v", claudeHeaderLogMaxValueBytes+10))
	lines, truncated := claudeHeaderSnapshot(h)
	require.True(t, truncated)
	require.Contains(t, lines[0], "[truncated len=")
	for i := 0; i < claudeHeaderLogMaxHeaders+5; i++ {
		h.Add("X-Many", "1")
	}
	lines, truncated = claudeHeaderSnapshot(h)
	require.True(t, truncated)
	require.LessOrEqual(t, len(lines), claudeHeaderLogMaxHeaders)
}
