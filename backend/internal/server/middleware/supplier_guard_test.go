//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSupplierRouteMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []string{"supplier", "admin", "user", ""} {
		for _, path := range []string{"/user/profile", "/user", "/user/password", "/user/totp/status", "/user/passkeys", "/keys", "/usage", "/user/aff", "/user/api-keys/1/usage/daily", "/subscriptions", "/payments"} {
			for _, backend := range []string{"true", "false"} {
				t.Run(role+path+backend, func(t *testing.T) {
					r := gin.New()
					r.Use(func(c *gin.Context) { c.Set(string(ContextKeyUserRole), role) })
					r.Use(BackendModeUserGuard(newBackendModeSettingService(t, backend)), SupplierSelfServiceGuard())
					r.GET(path, func(c *gin.Context) { c.Status(200) })
					w := httptest.NewRecorder()
					r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
					allowed := role == "admin" || (role != "supplier" && backend == "false") || (role == "supplier" && (path == "/user/profile" || path == "/user" || path == "/user/password" || path == "/user/totp/status" || path == "/user/passkeys"))
					if allowed {
						require.Equal(t, 200, w.Code)
					} else {
						require.Equal(t, 403, w.Code)
					}
				})
			}
		}
		t.Run("supplier-only-"+role, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Set(string(ContextKeyUserRole), role) }, SupplierOnly())
			r.GET("/supplier", func(c *gin.Context) { c.Status(200) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/supplier", nil))
			if role == "supplier" {
				require.Equal(t, 200, w.Code)
			} else {
				require.Equal(t, 403, w.Code)
			}
		})
	}
}

func TestSupplierDownstreamAPIKeyDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := &service.APIKey{ID: 100, UserID: 7, Key: "test-key", Status: service.StatusActive, User: &service.User{ID: 7, Role: "supplier", Status: service.StatusActive, Balance: 10}}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	for _, mode := range []string{config.RunModeSimple, config.RunModeStandard} {
		cfg := &config.Config{RunMode: mode}
		svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
		for _, auth := range []gin.HandlerFunc{gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)), APIKeyAuthGoogle(svc, cfg)} {
			r := gin.New()
			r.Use(auth)
			r.GET("/v1/usage", func(c *gin.Context) { c.Status(200) })
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("x-goog-api-key", "test-key")
			r.ServeHTTP(w, req)
			require.Equal(t, 403, w.Code)
		}
	}
}
