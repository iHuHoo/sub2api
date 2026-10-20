package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterClashSubscriptionRoutes(v1 *gin.RouterGroup, h *handler.Handlers, jwt middleware.JWTAuthMiddleware, audit middleware.AuditLogMiddleware, limiter *middleware.PanelRateLimiter) {
	if h.ClashSubscription == nil {
		return
	}
	v1.Any("/clash/subscription", h.ClashSubscription.Download)
	// Explicitly register the trailing-slash variant so Gin never redirects its query.
	v1.Any("/clash/subscription/", h.ClashSubscription.Download)
	for _, role := range []string{"admin", "supplier"} {
		guard := middleware.AdminOnly()
		if role == "supplier" {
			guard = middleware.SupplierOnly()
		}
		group := v1.Group("/"+role, gin.HandlerFunc(jwt), guard, limiter.Global(), gin.HandlerFunc(audit))
		group.GET("/clash-subscription", h.ClashSubscription.Manage)
		group.POST("/clash-subscription", h.ClashSubscription.Manage)
		group.POST("/clash-subscription/reset", h.ClashSubscription.Manage)
		group.DELETE("/clash-subscription", h.ClashSubscription.Manage)
	}
}
