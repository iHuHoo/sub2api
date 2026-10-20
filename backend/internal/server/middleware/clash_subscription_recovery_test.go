package middleware

import (
	"bytes"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net"
	"net/http/httptest"
	"os"
	"testing"
)

func TestClashSubscriptionPanicDoesNotLogCredentials(t *testing.T) {
	for _, target := range []string{"/api/v1/clash/subscription?token=fixture-bearer", "/api/v1/admin/clash-subscription"} {
		var logs bytes.Buffer
		r := gin.New()
		r.Use(recoveryWithWriter(&logs))
		r.GET("/api/v1/clash/subscription", func(c *gin.Context) {
			panic(&net.OpError{Err: &os.SyscallError{Syscall: "write", Err: errors.New("broken pipe fixture-password")}})
		})
		r.GET("/api/v1/admin/clash-subscription", func(c *gin.Context) { panic("fixture-password") })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		require.NotContains(t, logs.String(), "fixture-bearer")
		require.NotContains(t, logs.String(), "fixture-password")
		require.NotContains(t, w.Body.String(), "fixture-password")
	}
}
