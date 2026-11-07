//go:build unit

package handler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// A local rejection is one Info line with a fixed reason and no error text; any other
// forward error, including a wrapped upstream one, keeps the Error event (#153 logging G2).
func TestLogForwardErrorSeparatesLocalRejects(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	log := zap.New(core)

	local := service.NewLocalRejectError(service.LocalRejectNoCreditFastMode, errors.New("fast mode requires usage credits"))
	logForwardError(log, "gateway.forward_failed", fmt.Errorf("forward: %w", local), zap.Int64("account_id", 7), zap.Error(local))
	entries := logs.TakeAll()
	require.Len(t, entries, 1)
	require.Equal(t, zapcore.InfoLevel, entries[0].Level)
	require.Equal(t, "gateway.local_reject", entries[0].Message)
	got := entries[0].ContextMap()
	require.Equal(t, "no_credit_fast_mode", got["reason"])
	require.Equal(t, "local_reject", got["outcome"])
	require.Equal(t, "gateway.forward_failed", got["event"])
	require.EqualValues(t, 7, got["account_id"])
	require.NotContains(t, got, "error", "the error text stays out of the local-reject line")

	upstream := errors.New("upstream 529")
	logForwardError(log, "gateway.forward_failed", upstream, zap.Error(upstream))
	entries = logs.TakeAll()
	require.Len(t, entries, 1)
	require.Equal(t, zapcore.ErrorLevel, entries[0].Level)
	require.Equal(t, "gateway.forward_failed", entries[0].Message)
}
