//go:build unit

package admin

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
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
		{"seconds", "37", "37"}, {"zero", "0", "0"}, {"date", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), "date"},
		{"missing", "", "1"}, {"secret", "Bearer private-secret", "1"}, {"negative", "-3", "1"}, {"fraction", "1.5", "1"}, {"overflow", strings.Repeat("9", 64), "1"},
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
			r := gin.New()
			r.POST("/:id/diagnostics", NewProxyDiagnosticsHandler(svc).Check)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", "/1/diagnostics", strings.NewReader(`{"mode":"quick"}`)))
			require.Equal(t, 429, w.Code)
			if tc.want == "date" {
				require.Equal(t, tc.upstream, w.Header().Get("Retry-After"))
			} else {
				require.Equal(t, tc.want, w.Header().Get("Retry-After"))
			}
			require.NotContains(t, w.Body.String(), "private-secret")
		})
	}
}
