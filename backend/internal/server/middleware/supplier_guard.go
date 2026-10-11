package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"strings"
)

// SupplierOnly must follow JWT authentication; administrator routes remain separate.
func SupplierOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := GetUserRoleFromContext(c)
		if role != service.RoleSupplier {
			response.Forbidden(c, "Supplier access required")
			c.Abort()
			return
		}
		c.Next()
	}
}

func supplierSelfServicePath(path string) bool {
	path = strings.TrimPrefix(path, "/api/v1")
	switch path {
	case "/auth/me", "/auth/revoke-all-sessions", "/user", "/user/profile", "/user/password", "/user/passkeys":
		return true
	}
	return strings.HasPrefix(path, "/user/totp/") || strings.HasPrefix(path, "/user/passkeys/")
}

// SupplierSelfServiceGuard restricts suppliers to identity and account security routes.
func SupplierSelfServiceGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := GetUserRoleFromContext(c)
		if role == service.RoleSupplier && !supplierSelfServicePath(c.Request.URL.Path) {
			response.Forbidden(c, "Suppliers cannot access consumer services")
			c.Abort()
			return
		}
		c.Next()
	}
}
