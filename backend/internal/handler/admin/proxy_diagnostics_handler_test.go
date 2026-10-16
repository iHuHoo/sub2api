//go:build unit

package admin

import (
	"bytes"
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsHandlerInputAndCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, e := service.NewProxyDiagnosticsService(nil, nil, &config.Config{})
	require.NoError(t, e)
	h := NewProxyDiagnosticsHandler(svc)
	r := gin.New()
	r.GET("/capabilities", h.Capabilities)
	r.POST("/:id/diagnostics", h.Check)
	r.GET("/:id/diagnostics", h.Latest)
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/capabilities", "", 200},
		{"POST", "/1/diagnostics", `{"mode":"quick"}`, 503},
		{"POST", "/1/diagnostics", `{"mode":"sample"}`, 503},
		{"GET", "/1/diagnostics", "", 503},
		{"POST", "/1/diagnostics", `{"mode":"quick","proxy":{"password":"secret"}}`, 400},
		{"POST", "/1/diagnostics", `{"mode":"quick"} {}`, 400},
		{"POST", "/1/diagnostics", `{"mode":"other"}`, 400},
		{"POST", "/1/diagnostics", `null`, 400},
		{"POST", "/1/diagnostics", `{"Mode":"quick"}`, 400},
		{"POST", "/1/diagnostics", `{"mode":"quick","mode":"sample"}`, 400},
		{"POST", "/1/diagnostics", strings.Repeat("x", 1025), 400},
		{"POST", "/0/diagnostics", `{"mode":"quick"}`, 400},
		{"GET", "/bad/diagnostics", "", 400},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.ServeHTTP(w, req)
		require.Equal(t, tc.want, w.Code, tc.body+" "+w.Body.String())
		if tc.path == "/capabilities" {
			require.Contains(t, w.Body.String(), `"enabled":false`)
			require.NotContains(t, w.Body.String(), "token")
			require.NotContains(t, w.Body.String(), "base_url")
		}
	}
}

type diagnosticsHandlerRepo struct{ service.ProxyRepository }

func (diagnosticsHandlerRepo) GetByID(context.Context, int64) (*service.Proxy, error) {
	return &service.Proxy{ID: 1, Protocol: "http", Host: "proxy.example", Port: 8080}, nil
}
func TestDiagnosticsHandlerRetryAfter(t *testing.T) {
	t.Setenv("DIAGNOSTICS_TEST_TOKEN", "test-token")
	t.Setenv("DIAGNOSTICS_TEST_HMAC", strings.Repeat("h", 32))
	for _, tc := range []struct{ name, upstream, want string }{
		{"seconds", "37", "37"}, {"zero", "0", "1"}, {"date", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), "date"},
		{"missing", "", "1"}, {"secret", "Bearer private-secret", "1"}, {"negative", "-3", "1"}, {"fraction", "1.5", "1"}, {"overflow", strings.Repeat("9", 64), "1"}, {"duration overflow", "18446744075", "1"}, {"past date", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", tc.upstream)
				w.WriteHeader(429)
			}))
			defer server.Close()
			cfg := &config.Config{Subproxy: config.SubproxyConfig{Enabled: true, BaseURL: server.URL, AllowInternalHTTP: true, Token: config.SubproxySecretRef{Env: "DIAGNOSTICS_TEST_TOKEN"}, HMACSecret: config.SubproxySecretRef{Env: "DIAGNOSTICS_TEST_HMAC"}}}
			svc, e := service.NewProxyDiagnosticsService(diagnosticsHandlerRepo{}, nil, cfg)
			require.NoError(t, e)
			_, err := svc.Check(context.Background(), 1, "quick")
			require.Greater(t, service.RetryAfterSecondsFromError(err), 0)
			r := gin.New()
			r.POST("/:id/diagnostics", NewProxyDiagnosticsHandler(svc).Check)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", "/1/diagnostics", strings.NewReader(`{"mode":"quick"}`)))
			require.Equal(t, 429, w.Code)
			if tc.want == "date" {
				seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
				require.NoError(t, err)
				require.InDelta(t, 3600, seconds, 2)
			} else {
				require.Equal(t, tc.want, w.Header().Get("Retry-After"))
			}
			require.NotContains(t, w.Body.String(), "private-secret")
		})
	}
}

func TestDiagnosticsHandlerCancellation(t *testing.T) {
	t.Setenv("DIAGNOSTICS_TEST_TOKEN", "test-token")
	t.Setenv("DIAGNOSTICS_TEST_HMAC", strings.Repeat("h", 32))
	cfg := &config.Config{Subproxy: config.SubproxyConfig{Enabled: true, BaseURL: "http://127.0.0.1:1", AllowInternalHTTP: true, Token: config.SubproxySecretRef{Env: "DIAGNOSTICS_TEST_TOKEN"}, HMACSecret: config.SubproxySecretRef{Env: "DIAGNOSTICS_TEST_HMAC"}}}
	svc, err := service.NewProxyDiagnosticsService(diagnosticsHandlerRepo{}, nil, cfg)
	require.NoError(t, err)
	h := NewProxyDiagnosticsHandler(svc)
	r := gin.New()
	r.POST("/:id/diagnostics", h.Check)
	r.GET("/:id/diagnostics", h.Latest)
	for _, method := range []string{"POST", "GET"} {
		t.Run(method, func(t *testing.T) {
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(previous)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := httptest.NewRequest(method, "/1/diagnostics", strings.NewReader(`{"mode":"quick"}`)).WithContext(ctx)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, 499, w.Code)
			require.Empty(t, w.Body.String())
			require.NotContains(t, logs.String(), "ERROR")
		})
	}
}

func TestDiagnosticsHandlerDisabledDoesNotLogError(t *testing.T) {
	svc, err := service.NewProxyDiagnosticsService(nil, nil, &config.Config{})
	require.NoError(t, err)
	h := NewProxyDiagnosticsHandler(svc)
	r := gin.New()
	r.POST("/:id/diagnostics", h.Check)
	r.GET("/:id/diagnostics", h.Latest)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(previous)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(method, "/1/diagnostics", strings.NewReader(`{"mode":"quick"}`)))
			require.Equal(t, http.StatusServiceUnavailable, w.Code)
			require.Equal(t, `{"code":503,"message":"Proxy diagnostics is disabled","reason":"DIAGNOSTICS_DISABLED"}`, w.Body.String())
			require.NotContains(t, logs.String(), "ERROR")
		})
	}
}

func TestDiagnosticsHandlerUpstreamUnavailableStillLogsError(t *testing.T) {
	t.Setenv("DIAGNOSTICS_TEST_TOKEN", "test-token")
	t.Setenv("DIAGNOSTICS_TEST_HMAC", strings.Repeat("h", 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("private-upstream-detail"))
	}))
	defer server.Close()
	cfg := &config.Config{Subproxy: config.SubproxyConfig{Enabled: true, BaseURL: server.URL, AllowInternalHTTP: true, Token: config.SubproxySecretRef{Env: "DIAGNOSTICS_TEST_TOKEN"}, HMACSecret: config.SubproxySecretRef{Env: "DIAGNOSTICS_TEST_HMAC"}}}
	svc, err := service.NewProxyDiagnosticsService(diagnosticsHandlerRepo{}, nil, cfg)
	require.NoError(t, err)
	r := gin.New()
	r.POST("/:id/diagnostics", NewProxyDiagnosticsHandler(svc).Check)
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/1/diagnostics", strings.NewReader(`{"mode":"quick"}`)))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, `{"code":503,"message":"Diagnostics service unavailable","reason":"service_unavailable"}`, w.Body.String())
	require.Contains(t, logs.String(), "[ERROR] POST /1/diagnostics")
	require.NotContains(t, logs.String(), "private-upstream-detail")
}
