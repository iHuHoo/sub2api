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

// logForwardError logs a failed forward: a local rejection via logLocalReject, every other
// error as the caller's Error event.
func logForwardError(reqLog *zap.Logger, event string, err error, fields ...zap.Field) {
	if !logLocalReject(reqLog, event, err, fields...) {
		reqLog.Error(event, fields...)
	}
}

// logLocalReject logs a request the gateway rejected itself before any upstream call. It is a
// client error: one Info gateway.local_reject line with its fixed reason and without the
// error text. It reports false, logging nothing, for any other error.
func logLocalReject(reqLog *zap.Logger, event string, err error, fields ...zap.Field) bool {
	reason, ok := service.LocalRejectReason(err)
	if !ok {
		return false
	}
	kept := []zap.Field{zap.String("event", event), zap.String("outcome", "local_reject"), zap.String("reason", reason)}
	for _, f := range fields {
		if f.Key != "error" {
			kept = append(kept, f)
		}
	}
	reqLog.Info("gateway.local_reject", kept...)
	return true
}
