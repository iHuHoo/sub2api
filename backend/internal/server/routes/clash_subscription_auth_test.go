package routes

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

type clashSettings struct{ service.SettingRepository }

func (clashSettings) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}
func TestClashSubscriptionAdminComplianceGuard(t *testing.T) {
	settings := service.NewSettingService(clashSettings{}, &config.Config{})
	svc := service.NewClashSubscriptionService(nil, &config.Config{})
	h := &handler.Handlers{ClashSubscription: handler.NewClashSubscriptionHandler(svc, nil)}
	jwt := middleware.JWTAuthMiddleware(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Set(string(middleware.ContextKeyUserRole), "admin")
		c.Next()
	})
	audit := middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	r := gin.New()
	RegisterClashSubscriptionRoutes(r.Group("/api/v1"), h, jwt, audit, middleware.NewPanelRateLimiter(nil, nil), settings, middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() }))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/admin/clash-subscription", nil))
	require.Equal(t, 423, w.Code)
	require.Contains(t, w.Body.String(), "ADMIN_COMPLIANCE_ACK_REQUIRED")
}
