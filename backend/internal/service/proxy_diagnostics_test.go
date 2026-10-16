//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/config"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type diagnosticsRepo struct {
	ProxyRepository
	mu sync.Mutex
	p  *Proxy
}

func (r *diagnosticsRepo) GetByID(context.Context, int64) (*Proxy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.p == nil {
		return nil, ErrProxyNotFound
	}
	p := *r.p
	return &p, nil
}

type diagnosticsMemoryCache struct {
	mu       sync.Mutex
	sequence int64
	fp       string
	report   CachedDiagnosticsReport
	fail     bool
}

func (c *diagnosticsMemoryCache) Reserve(_ context.Context, _ int64, fp string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return "", errors.New("redis down")
	}
	c.sequence++
	c.fp = fp
	return strconv.FormatInt(c.sequence, 10), nil
}
func (c *diagnosticsMemoryCache) CommitIfLatest(_ context.Context, _ int64, seq string, fp string, r json.RawMessage) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return false, errors.New("redis down")
	}
	if seq != strconv.FormatInt(c.sequence, 10) || fp != c.fp {
		return false, nil
	}
	c.report = CachedDiagnosticsReport{Sequence: seq, Fingerprint: fp, Report: r}
	return true, nil
}
func (c *diagnosticsMemoryCache) Get(context.Context, int64) (CachedDiagnosticsReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return CachedDiagnosticsReport{}, errors.New("redis down")
	}
	return c.report, nil
}
func diagnosticsFixture(t *testing.T, id, mode string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/subproxy/complete.json")
	require.NoError(t, e)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(b, &obj))
	obj["request_id"] = id
	obj["mode"] = mode
	b, e = json.Marshal(obj)
	require.NoError(t, e)
	return b
}
func diagnosticsTestService(t *testing.T, h http.HandlerFunc) (*ProxyDiagnosticsService, *diagnosticsRepo, *diagnosticsMemoryCache) {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	t.Setenv("I1_TOKEN", "test-service-token")
	t.Setenv("I1_HMAC", strings.Repeat("h", 32))
	cfg := &config.Config{Subproxy: config.SubproxyConfig{Enabled: true, BaseURL: server.URL, AllowInternalHTTP: true, Token: config.SubproxySecretRef{Env: "I1_TOKEN"}, HMACSecret: config.SubproxySecretRef{Env: "I1_HMAC"}}}
	repo := &diagnosticsRepo{p: &Proxy{ID: 1, Protocol: "http", Host: "proxy.example", Port: 8080, Username: "proxy-user", Password: "proxy-password"}}
	cache := &diagnosticsMemoryCache{}
	s, e := NewProxyDiagnosticsService(repo, cache, cfg)
	require.NoError(t, e)
	t.Cleanup(s.client.CloseIdleConnections)
	return s, repo, cache
}
func diagnosticsReply(t *testing.T, w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID string           `json:"request_id"`
		Mode      string           `json:"mode"`
		Proxy     diagnosticsProxy `json:"proxy"`
	}
	if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
		t.Error(e)
		w.WriteHeader(500)
		return
	}
	if r.URL.Path != "/v1/checks" || r.Header.Get("Authorization") != "Bearer test-service-token" || req.Proxy.Password != "proxy-password" {
		t.Error("invalid subproxy request")
	}
	w.Write(diagnosticsFixture(t, req.RequestID, req.Mode))
}
func TestDiagnosticsServiceCheckLatestAndRotation(t *testing.T) {
	s, repo, cache := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) { diagnosticsReply(t, w, r) })
	ctx := context.Background()
	_, e := s.Latest(ctx, 1)
	require.ErrorIs(t, e, ErrDiagnosticsReportNotFound)
	got, e := s.Check(ctx, 1, "sample")
	require.NoError(t, e)
	require.True(t, got.Saved)
	require.NotContains(t, string(got.Report), "proxy-password")
	require.NotContains(t, string(got.Report), "test-service-token")
	latest, e := s.Latest(ctx, 1)
	require.NoError(t, e)
	require.JSONEq(t, string(got.Report), string(latest.Report))
	s.token = "rotated"
	_, e = s.Latest(ctx, 1)
	require.ErrorIs(t, e, ErrDiagnosticsReportNotFound)
	s.token = "test-service-token"
	repo.p.Port++
	_, e = s.Latest(ctx, 1)
	require.ErrorIs(t, e, ErrDiagnosticsReportNotFound)
	repo.p.Port--
	cache.fail = true
	got, e = s.Check(ctx, 1, "quick")
	require.NoError(t, e)
	require.False(t, got.Saved)
}
func TestDiagnosticsServiceChangedOrDeleted(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "deleted"}[deleted], func(t *testing.T) {
			var repo *diagnosticsRepo
			s, r, _ := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) {
				repo.mu.Lock()
				if deleted {
					repo.p = nil
				} else {
					repo.p.Password = "changed"
				}
				repo.mu.Unlock()
				diagnosticsReply(t, w, r)
			})
			repo = r
			_, e := s.Check(context.Background(), 1, "quick")
			if deleted {
				require.ErrorIs(t, e, ErrProxyNotFound)
			} else {
				require.ErrorIs(t, e, ErrDiagnosticsProxyChanged)
			}
		})
	}
}
func TestDiagnosticsServiceOutOfOrder(t *testing.T) {
	first := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	s, _, _ := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(first)
			<-release
		}
		diagnosticsReply(t, w, r)
	})
	type outcome struct {
		r DiagnosticsResponse
		e error
	}
	done := make(chan outcome, 1)
	go func() { r, e := s.Check(context.Background(), 1, "quick"); done <- outcome{r, e} }()
	<-first
	newer, e := s.Check(context.Background(), 1, "quick")
	require.NoError(t, e)
	require.True(t, newer.Saved)
	close(release)
	old := <-done
	require.NoError(t, old.e)
	require.False(t, old.r.Saved)
	latest, e := s.Latest(context.Background(), 1)
	require.NoError(t, e)
	require.JSONEq(t, string(newer.Report), string(latest.Report))
}
func TestDiagnosticsServiceRemoteErrors(t *testing.T) {
	for _, tc := range []struct {
		status       int
		body, reason string
	}{
		{401, "proxy-password", "service_auth_failed"}, {429, "proxy-password", "busy"}, {503, "proxy-password", "service_unavailable"},
		{400, `{"error":{"code":"proxy_address_unsupported","message":"proxy-password"}}`, "proxy_address_unsupported"},
		{400, `{"error":{"code":"request_invalid","message":"proxy-password"}}`, "request_invalid"},
		{400, `{"error":{"code":"unrecognized","message":"proxy-password"}}`, "service_unavailable"},
		{302, "proxy-password", "service_unavailable"}, {200, `{}`, "service_unavailable"},
	} {
		t.Run(tc.reason+http.StatusText(tc.status), func(t *testing.T) {
			s, _, _ := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://127.0.0.1:1/")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			_, e := s.Check(context.Background(), 1, "quick")
			require.Equal(t, tc.reason, apperrors.Reason(e))
			if tc.reason == "request_invalid" {
				require.Equal(t, 400, apperrors.Code(e))
			}
			require.NotContains(t, e.Error(), "proxy-password")
		})
	}
}
func TestDiagnosticsServiceCancellationAndTimeout(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		t.Run(map[bool]string{true: "caller", false: "client"}[cancel], func(t *testing.T) {
			entered := make(chan struct{})
			ended := make(chan struct{})
			s, _, _ := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(entered)
				<-r.Context().Done()
				close(ended)
			})
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			if !cancel {
				s.client.Timeout = 30 * time.Millisecond
			}
			done := make(chan error, 1)
			go func() { _, e := s.Check(ctx, 1, "quick"); done <- e }()
			<-entered
			if cancel {
				stop()
			}
			if cancel {
				require.ErrorIs(t, <-done, context.Canceled)
			} else {
				require.ErrorIs(t, <-done, ErrDiagnosticsUnavailable)
			}
			select {
			case <-ended:
			case <-time.After(time.Second):
				t.Fatal("caller cancellation did not reach subproxy")
			}
		})
	}
}

func TestDiagnosticsUnsupportedSchemaHasDedicatedReason(t *testing.T) {
	// A newer major need not have v1 fields; identify it before validating v1.
	s, repo, cache := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":2}`))
	})
	_, err := s.Check(context.Background(), 1, "quick")
	require.Equal(t, "schema_unsupported", apperrors.Reason(err))
	cache.report = CachedDiagnosticsReport{Fingerprint: s.fingerprint(repo.p), Report: json.RawMessage(`{"schema_version":2}`)}
	_, err = s.Latest(context.Background(), 1)
	require.Equal(t, "schema_unsupported", apperrors.Reason(err))
}
func TestDiagnosticsDisabledHasNoClientOrSecrets(t *testing.T) {
	s, e := NewProxyDiagnosticsService(nil, nil, &config.Config{Subproxy: config.SubproxyConfig{BaseURL: ":invalid", Token: config.SubproxySecretRef{File: "/missing"}}})
	require.NoError(t, e)
	require.Nil(t, s.client)
	_, e = s.Check(context.Background(), 1, "quick")
	require.ErrorIs(t, e, ErrDiagnosticsDisabled)
	_, e = s.Latest(context.Background(), 1)
	require.ErrorIs(t, e, ErrDiagnosticsDisabled)
}

// Cancellation at either HTTP boundary must preserve the last completed report.
func TestDiagnosticsCancellationDuringHTTP(t *testing.T) {
	for _, body := range []bool{false, true} {
		t.Run(strconv.FormatBool(body), func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(previous)
			entered := make(chan struct{})
			release := make(chan struct{})
			s, _, cache := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if body {
					w.WriteHeader(200)
					_, _ = w.Write([]byte(`{"schema_version":`))
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-release
			})
			defer close(release)
			cache.report = CachedDiagnosticsReport{Report: json.RawMessage(`{"previous":true}`)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := s.Check(ctx, 1, "quick"); done <- err }()
			<-entered
			cancel()
			err := <-done
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, `{"previous":true}`, string(cache.report.Report))
			require.Contains(t, logs.String(), `"classification":"cancelled"`)
			require.NotContains(t, logs.String(), `"level":"WARN"`)
			require.NotContains(t, logs.String(), `"level":"ERROR"`)
			require.NotContains(t, logs.String(), "proxy-password")
			require.NotContains(t, logs.String(), "test-service-token")
		})
	}
}

type diagnosticsCancelRepo struct {
	ProxyRepository
	cancel    context.CancelFunc
	calls, at int
}

func (r *diagnosticsCancelRepo) GetByID(ctx context.Context, id int64) (*Proxy, error) {
	r.calls++
	if r.calls == r.at {
		r.cancel()
	}
	return r.ProxyRepository.GetByID(ctx, id)
}
func TestDiagnosticsCancellationAfterProxyRead(t *testing.T) {
	for _, at := range []int{1, 2} {
		t.Run(strconv.Itoa(at), func(t *testing.T) {
			s, repo, cache := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) { diagnosticsReply(t, w, r) })
			cache.report = CachedDiagnosticsReport{Report: json.RawMessage(`{"previous":true}`)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.repo = &diagnosticsCancelRepo{ProxyRepository: repo, cancel: cancel, at: at}
			_, err := s.Check(ctx, 1, "quick")
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, `{"previous":true}`, string(cache.report.Report))
		})
	}
}

type diagnosticsCancelCache struct {
	ProxyDiagnosticsCache
	cancel context.CancelFunc
	stage  string
}

func (c diagnosticsCancelCache) Reserve(ctx context.Context, id int64, fp string) (string, error) {
	if c.stage == "reserve" {
		c.cancel()
		return "", ctx.Err()
	}
	return c.ProxyDiagnosticsCache.Reserve(ctx, id, fp)
}
func (c diagnosticsCancelCache) CommitIfLatest(ctx context.Context, id int64, seq, fp string, r json.RawMessage) (bool, error) {
	if c.stage == "commit" {
		c.cancel()
		return false, ctx.Err()
	}
	return c.ProxyDiagnosticsCache.CommitIfLatest(ctx, id, seq, fp, r)
}
func (c diagnosticsCancelCache) Get(ctx context.Context, id int64) (CachedDiagnosticsReport, error) {
	c.cancel()
	return CachedDiagnosticsReport{}, ctx.Err()
}
func TestDiagnosticsCancellationDuringCache(t *testing.T) {
	for _, stage := range []string{"reserve", "commit", "latest"} {
		t.Run(stage, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(previous)
			s, _, cache := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) { diagnosticsReply(t, w, r) })
			cache.report = CachedDiagnosticsReport{Report: json.RawMessage(`{"previous":true}`)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.cache = diagnosticsCancelCache{cache, cancel, stage}
			var err error
			if stage == "latest" {
				_, err = s.Latest(ctx, 1)
			} else {
				_, err = s.Check(ctx, 1, "quick")
			}
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, `{"previous":true}`, string(cache.report.Report))
			require.Contains(t, logs.String(), `"classification":"cancelled"`)
			require.NotContains(t, logs.String(), `"level":"WARN"`)
		})
	}
}

func TestDiagnosticsCallerDeadlineRemainsFailure(t *testing.T) {
	s, _, _ := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) {})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := s.Check(ctx, 1, "quick")
	require.ErrorIs(t, err, ErrDiagnosticsUnavailable)
}
