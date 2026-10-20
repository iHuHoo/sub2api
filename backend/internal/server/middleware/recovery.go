package middleware

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// Recovery converts panics into the project's standard JSON error envelope.
//
// It preserves Gin's broken-pipe handling by not attempting to write a response
// when the client connection is already gone.
func Recovery() gin.HandlerFunc {
	return recoveryWithWriter(gin.DefaultErrorWriter)
}

func recoveryWithWriter(writer io.Writer) gin.HandlerFunc {
	standard := gin.CustomRecoveryWithWriter(writer, func(c *gin.Context, recovered any) {
		recoveredErr, _ := recovered.(error)

		if isBrokenPipe(recoveredErr) {
			if recoveredErr != nil {
				_ = c.Error(recoveredErr)
			}
			c.Abort()
			return
		}

		if c.Writer.Written() {
			c.Abort()
			return
		}

		response.ErrorWithDetails(
			c,
			http.StatusInternalServerError,
			infraerrors.UnknownMessage,
			infraerrors.UnknownReason,
			nil,
		)
		c.Abort()
	})
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if !strings.HasPrefix(p, "/api/v1/clash/") && p != "/api/v1/admin/clash-subscription" && p != "/api/v1/admin/clash-subscription/reset" && p != "/api/v1/supplier/clash-subscription" && p != "/api/v1/supplier/clash-subscription/reset" {
			standard(c)
			return
		}
		// Gin's standard recovery dumps the request URI and panic value, either
		// of which can contain bearer URLs or exported credentials on this path.
		defer func() {
			if recover() != nil {
				_, _ = io.WriteString(writer, "clash subscription request panicked; sensitive details omitted\n")
				c.Header("Cache-Control", "private, no-store")
				c.Header("Referrer-Policy", "no-referrer")
				c.Header("X-Content-Type-Options", "nosniff")
				if strings.HasPrefix(p, "/api/v1/clash/") {
					for key := range c.Writer.Header() {
						if strings.HasPrefix(strings.ToLower(key), "access-control-allow-") || strings.EqualFold(key, "Access-Control-Expose-Headers") {
							c.Writer.Header().Del(key)
						}
					}
				}
				if !c.Writer.Written() {
					c.Status(http.StatusServiceUnavailable)
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}

	var syscallErr *os.SyscallError
	if !errors.As(opErr.Err, &syscallErr) {
		return false
	}

	msg := strings.ToLower(syscallErr.Error())
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
}
