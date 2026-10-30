//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func captureProxyLogs(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "output.log")
	var previous logger.InitOptions
	require.NoError(t, logger.Reconfigure(func(options *logger.InitOptions) error {
		previous = *options
		*options = logger.InitOptions{Level: "debug", Format: "json", Output: logger.OutputOptions{ToFile: true, FilePath: path}}
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, logger.Init(previous)) })
	return func() string {
		logger.Sync()
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(data)
	}
}

func assertProxyCredentialsAbsent(t *testing.T, text, username, password string) {
	t.Helper()
	for _, value := range []string{username, password, url.User(username).String(), url.UserPassword(username, password).String(), url.QueryEscape(username), url.QueryEscape(password)} {
		require.NotContains(t, text, value)
	}
}

func TestOpenAIWSInvalidProxyCredentials(t *testing.T) {
	for _, userinfo := range []string{"fictional-user:fictional-password", "fictional%40user:fictional%3Ap%40ss%2Fword"} {
		t.Run(userinfo, func(t *testing.T) {
			dialer := newDefaultOpenAIWSClientDialer()
			conn, status, headers, err := dialer.Dial(context.Background(), "wss://upstream.invalid", nil, "http://"+userinfo+"@proxy.invalid:bad-port")
			require.Error(t, err)
			require.Nil(t, conn)
			require.Zero(t, status)
			require.Nil(t, headers)
			assertProxyCredentialsAbsent(t, err.Error(), "fictional-user", "fictional-password")
			assertProxyCredentialsAbsent(t, err.Error(), "fictional@user", "fictional:p@ss/word")
			require.EqualError(t, err, "invalid proxy URL")
			require.Nil(t, errors.Unwrap(err))
		})
	}
}

func TestForwardProxyCredentialLogging(t *testing.T) {
	logs := captureProxyLogs(t)
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_test","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`))}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	account := newAnthropicOAuthAccountForPartialUsageTest()
	account.ProxyID = new(int64(701))
	account.Proxy = &Proxy{ID: 701, Protocol: "http", Host: "proxy.invalid", Port: 8080, Username: "fictional@user", Password: "fictional:p@ss/word"}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-3-5-sonnet-latest","messages":[{"role":"user","content":"hello"}],"max_tokens":10}`)), PlatformAnthropic)
	require.NoError(t, err)
	_, err = svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	text := logs()
	require.Contains(t, text, "[Forward] Using account")
	assertProxyCredentialsAbsent(t, text, account.Proxy.Username, account.Proxy.Password)
}

func TestGeminiOAuthProxyCredentialLogging(t *testing.T) {
	logs := captureProxyLogs(t)
	username, password := "fictional@user", "fictional:p@ss/word"
	proxyURL := "http://" + url.UserPassword(username, password).String() + "@proxy.invalid:8080"
	called := false
	client := &mockGeminiOAuthClient{exchangeCodeFunc: func(_ context.Context, _, _, _, _, got string) (*geminicli.TokenResponse, error) {
		called = true
		require.Equal(t, proxyURL, got)
		return nil, errors.New("offline exchange stopped")
	}}
	svc := NewGeminiOAuthService(nil, client, nil, nil, &config.Config{})
	t.Cleanup(svc.Stop)
	svc.sessionStore.Set("proxy-log-test", &geminicli.OAuthSession{State: "test-state", ProxyURL: proxyURL, OAuthType: "code_assist", CreatedAt: time.Now()})
	_, err := svc.ExchangeCode(context.Background(), &GeminiExchangeCodeInput{SessionID: "proxy-log-test", State: "test-state", Code: "fictional-code"})
	require.ErrorContains(t, err, "offline exchange stopped")
	require.True(t, called)
	assertProxyCredentialsAbsent(t, logs(), username, password)
}

func TestSlogProxyUserinfoOpsSink(t *testing.T) {
	captureProxyLogs(t)
	var captured []*OpsInsertSystemLogInput
	repo := &opsRepoMock{BatchInsertSystemLogsFn: func(_ context.Context, rows []*OpsInsertSystemLogInput) (int64, error) {
		captured = append(captured, rows...)
		return int64(len(rows)), nil
	}}
	sink := NewOpsSystemLogSink(repo)
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	slog.Warn("proxy failure http://fictional-user:fictional-pass@proxy.invalid", "proxy", "socks5h://fictional-user:fictional-pass@proxy.invalid")
	select {
	case event := <-sink.queue:
		require.Equal(t, "warn", event.Level)
		_, err := sink.flushBatch(context.Background(), []*logger.LogEvent{event})
		require.NoError(t, err)
	default:
		t.Fatal("slog warning did not reach ops sink")
	}
	require.Len(t, captured, 1)
	assertProxyCredentialsAbsent(t, captured[0].Message+captured[0].ExtraJSON, "fictional-user", "fictional-pass")
}
