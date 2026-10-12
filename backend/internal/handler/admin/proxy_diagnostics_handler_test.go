//go:build unit

package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
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
