//go:build unit

package routes

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

type diagnosticsUsers struct {
	service.UserRepository
	user *service.User
}

func (r diagnosticsUsers) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func (r diagnosticsUsers) GetByID(context.Context, int64) (*service.User, error) { return r.user, nil }

func TestDiagnosticsAdminOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, role := range []string{"admin", "supplier", "user", ""} {
		t.Run(role, func(t *testing.T) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-secret", ExpireHour: 1}}
			auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			user := &service.User{ID: 1, Role: role, Status: service.StatusActive}
			users := service.NewUserService(diagnosticsUsers{user: user}, nil, nil, nil)
			svc, err := service.NewProxyDiagnosticsService(nil, nil, cfg)
			require.NoError(t, err)
			h := &handler.Handlers{Admin: &handler.AdminHandlers{Proxy: &admin.ProxyHandler{}, ProxyDiagnostics: admin.NewProxyDiagnosticsHandler(svc)}}
			r := gin.New()
			RegisterAdminRoutes(r.Group("/api/v1"), h, middleware.NewAdminAuthMiddleware(auth, users, nil, nil), middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() }), nil, nil)
			for _, tc := range []struct {
				method, path string
				status       int
			}{{"POST", "/1/diagnostics", 503}, {"GET", "/1/diagnostics", 503}, {"GET", "/diagnostics-capabilities", 200}} {
				req := httptest.NewRequest(tc.method, "/api/v1/admin/proxies"+tc.path, strings.NewReader(`{"mode":"quick"}`))
				req.Header.Set("Content-Type", "application/json")
				if role != "" {
					token, e := auth.GenerateToken(context.Background(), user)
					require.NoError(t, e)
					req.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				want := tc.status
				if role == "" {
					want = 401
				} else if role != "admin" {
					want = 403
				}
				require.Equal(t, want, w.Code, w.Body.String())
			}
		})
	}
}
