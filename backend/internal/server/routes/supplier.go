package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func RegisterSupplierRoutes(v1 *gin.RouterGroup, h *handler.Handlers, jwtAuth middleware.JWTAuthMiddleware, auditLog middleware.AuditLogMiddleware, limiter *middleware.PanelRateLimiter) {
	supplier := v1.Group("/supplier")
	supplier.Use(gin.HandlerFunc(jwtAuth), middleware.SupplierOnly(), limiter.Global(), gin.HandlerFunc(auditLog))
	supplier.GET("/accounts", h.Supplier.ListAccounts)
	supplier.POST("/accounts", h.Supplier.CreateAccount)
	supplier.POST("/accounts/import-codex-session", limiter.Heavy(), h.Supplier.ImportCodexSession)
	supplier.GET("/accounts/:id", h.Supplier.GetAccount)
	supplier.GET("/accounts/:id/usage", limiter.Heavy(), h.Supplier.GetAccountUsage)
	supplier.PUT("/accounts/:id", h.Supplier.UpdateAccount)
	supplier.POST("/accounts/:id/pause", h.Supplier.PauseAccount)
	supplier.GET("/proxies", h.Supplier.ListProxies)
	supplier.POST("/proxies", h.Supplier.CreateProxy)
	supplier.GET("/proxies/:id", h.Supplier.GetProxy)
	supplier.PUT("/proxies/:id", h.Supplier.UpdateProxy)
	supplier.DELETE("/proxies/:id", h.Supplier.DeleteProxy)
	supplier.POST("/proxies/:id/test", limiter.Heavy(), h.Supplier.TestProxy)
	supplier.GET("/usage", limiter.Heavy(), h.Supplier.Usage)
	supplier.POST("/oauth/:platform/:action", limiter.Heavy(), h.Supplier.OAuth)
	supplier.GET("/oauth/:platform/capabilities", h.Supplier.OAuthCapabilities)
}
