//go:build unit

package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestSupplierRoutesRoleBoundaryAndNoAccountDelete(t *testing.T) {
	for _, role := range []string{service.RoleUser, service.RoleAdmin, ""} {
		r := gin.New()
		h := &handler.Handlers{Supplier: handler.NewSupplierHandler(nil)}
		auth := middleware.JWTAuthMiddleware(func(c *gin.Context) { c.Set(string(middleware.ContextKeyUserRole), role); c.Next() })
		audit := middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
		RegisterSupplierRoutes(r.Group("/api/v1"), h, auth, audit, nil)
		for _, path := range []string{"/api/v1/supplier/accounts", "/api/v1/supplier/proxies", "/api/v1/supplier/usage"} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			require.Equal(t, 403, w.Code)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/supplier/accounts/1", nil))
		require.Equal(t, 404, w.Code)
		for _, route := range r.Routes() {
			require.NotContains(t, route.Path, "oauth")
			require.NotContains(t, route.Path, "admin")
		}
	}
}
