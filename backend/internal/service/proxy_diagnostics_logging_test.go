//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type diagnosticsLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *diagnosticsLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *diagnosticsLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
func captureDiagnosticsLogs(t *testing.T) *diagnosticsLogBuffer {
	t.Helper()
	b := &diagnosticsLogBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(b, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return b
}
func requireDiagnosticsWarning(t *testing.T, logs *diagnosticsLogBuffer, classification string) {
	t.Helper()
	found := false
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record["msg"] != "proxy_diagnostics_failed" {
			continue
		}
		require.Len(t, record, 5) // Only standard time/level/message and the two safe fields.
		require.Equal(t, "WARN", record["level"])
		require.Equal(t, float64(1), record["proxy_id"])
		if record["classification"] == classification {
			found = true
		}
	}
	require.True(t, found, "missing warning %s: %s", classification, logs.String())
	for _, secret := range []string{"proxy-password", "proxy-user", "test-service-token", "proxy.example", "raw-secret"} {
		require.NotContains(t, logs.String(), secret)
	}
}
func TestDiagnosticsFailureWarnings(t *testing.T) {
	for _, tc := range []struct {
		status               int
		body, classification string
	}{
		{401, "raw-secret", "service_auth_failed"}, {429, "raw-secret", "busy"}, {503, "raw-secret", "service_unavailable"},
		{400, `{"error":{"code":"request_invalid","message":"raw-secret"}}`, "request_invalid"},
		{400, `{"error":{"code":"proxy_address_unsupported","message":"raw-secret"}}`, "proxy_address_unsupported"},
		{200, `{"password":"raw-secret"}`, "report_invalid"},
	} {
		t.Run(tc.classification, func(t *testing.T) {
			logs := captureDiagnosticsLogs(t)
			s, _, _ := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) })
			_, err := s.Check(context.Background(), 1, "quick")
			require.Error(t, err)
			requireDiagnosticsWarning(t, logs, tc.classification)
		})
	}
}
func TestDiagnosticsCacheFailureWarnings(t *testing.T) {
	for _, stage := range []string{"reserve", "commit", "read"} {
		t.Run(stage, func(t *testing.T) {
			logs := captureDiagnosticsLogs(t)
			var cache *diagnosticsMemoryCache
			s, _, c := diagnosticsTestService(t, func(w http.ResponseWriter, r *http.Request) {
				if stage == "commit" {
					cache.mu.Lock()
					cache.fail = true
					cache.mu.Unlock()
				}
				diagnosticsReply(t, w, r)
			})
			cache = c
			if stage != "commit" {
				cache.fail = true
			}
			if stage == "read" {
				_, err := s.Latest(context.Background(), 1)
				require.ErrorIs(t, err, ErrDiagnosticsReportNotFound)
			} else {
				result, err := s.Check(context.Background(), 1, "quick")
				require.NoError(t, err)
				require.False(t, result.Saved)
				require.NotEmpty(t, result.Report)
			}
			requireDiagnosticsWarning(t, logs, "cache_"+stage+"_failed")
		})
	}
}
