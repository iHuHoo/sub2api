package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestClashSubscriptionDownloadBoundary(t *testing.T) {
	s := service.NewClashSubscriptionService(nil, &config.Config{ClashSubscription: config.ClashSubscriptionConfig{Enabled: true, PublicBaseURL: "https://api.example"}})
	h := NewClashSubscriptionHandler(s, nil)
	r := gin.New()
	r.Use(middleware.CORS(config.CORSConfig{AllowedOrigins: []string{"*"}}))
	r.Any("/api/v1/clash/subscription", h.Download)
	for _, tc := range []struct {
		method, host string
		status       int
	}{{"GET", "evil.example", 404}, {"POST", "api.example", 405}, {"GET", "api.example", 503}, {"OPTIONS", "api.example", 204}} {
		req := httptest.NewRequest(tc.method, "https://"+tc.host+"/api/v1/clash/subscription?token=secret", nil)
		req.Header.Set("Origin", "https://example.org")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, tc.status, w.Code)
		require.NotContains(t, w.Body.String(), "secret")
		require.Empty(t, w.Header().Get("Location"))
		if tc.method != "OPTIONS" {
			require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
			require.Contains(t, w.Header().Get("Cache-Control"), "no-store")
		}
	}
}
