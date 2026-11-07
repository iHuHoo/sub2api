package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func requestLogger(c *gin.Context, component string, fields ...zap.Field) *zap.Logger {
	base := logger.L()
	if c != nil && c.Request != nil {
		base = logger.FromContext(c.Request.Context())
	}

	if component != "" {
		fields = append([]zap.Field{zap.String("component", component)}, fields...)
	}
	return base.With(fields...)
}

// logForwardError logs a failed forward. A request the gateway rejected itself before any
// upstream call is a client error: it is logged once at Info as gateway.local_reject with
// its fixed reason and without the error text. Every other error keeps the caller's event.
func logForwardError(reqLog *zap.Logger, event string, err error, fields ...zap.Field) {
	reason, ok := service.LocalRejectReason(err)
	if !ok {
		reqLog.Error(event, fields...)
		return
	}
	kept := []zap.Field{zap.String("event", event), zap.String("outcome", "local_reject"), zap.String("reason", reason)}
	for _, f := range fields {
		if f.Key != "error" {
			kept = append(kept, f)
		}
	}
	reqLog.Info("gateway.local_reject", kept...)
}
