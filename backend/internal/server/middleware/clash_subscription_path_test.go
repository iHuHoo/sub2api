package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClashSubscriptionPathNoRedirect(t *testing.T) {
	r := gin.New()
	r.GET("/api/v1/clash/subscription", func(c *gin.Context) { c.Status(200) })
	h := ClashSubscriptionPathGuard(r)
	for _, p := range []string{"/api/v1/clash/subscription/", "/api//v1/clash/subscription", "/api/v1/clash/%73ubscription", "/API/v1/clash/subscription", "/api/v1/clash/x/../subscription"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p+"?token=fixture-secret", nil))
		require.Equal(t, 404, w.Code)
		require.Empty(t, w.Header().Get("Location"))
		require.NotContains(t, w.Body.String(), "fixture-secret")
	}
}
