//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func newTestDiagnosticsCache(t *testing.T) (service.ProxyDiagnosticsCache, *miniredis.Miniredis) {
	t.Helper()
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr(), MaxRetries: -1})
	t.Cleanup(func() { client.Close() })
	return NewProxyDiagnosticsCache(client), r
}
func testDiagnosticsReport(t *testing.T, id string) json.RawMessage {
	b, e := os.ReadFile("../service/testdata/subproxy/complete.json")
	require.NoError(t, e)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(b, &obj))
	obj["request_id"] = id
	b, e = json.Marshal(obj)
	require.NoError(t, e)
	return b
}
func TestDiagnosticsCacheGenerations(t *testing.T) {
	c, r := newTestDiagnosticsCache(t)
	ctx := context.Background()
	a, e := c.Reserve(ctx, 1, "fingerprint")
	require.NoError(t, e)
	b, e := c.Reserve(ctx, 1, "fingerprint")
	require.NoError(t, e)
	ok, e := c.CommitIfLatest(ctx, 1, b, "fingerprint", testDiagnosticsReport(t, "new"))
	require.NoError(t, e)
	require.True(t, ok)
	ok, e = c.CommitIfLatest(ctx, 1, a, "fingerprint", testDiagnosticsReport(t, "old"))
	require.NoError(t, e)
	require.False(t, ok)
	got, e := c.Get(ctx, 1)
	require.NoError(t, e)
	require.Contains(t, string(got.Report), `"new"`)
	ok, e = c.CommitIfLatest(ctx, 1, b, "different", testDiagnosticsReport(t, "wrong"))
	require.NoError(t, e)
	require.False(t, ok)
	require.Equal(t, 24*time.Hour, r.TTL(diagnosticsKeys(1)[2]))
	r.Del(diagnosticsKeys(1)[1])
	ok, e = c.CommitIfLatest(ctx, 1, b, "fingerprint", testDiagnosticsReport(t, "missing"))
	require.NoError(t, e)
	require.False(t, ok)
	r.FastForward(24 * time.Hour)
	_, e = c.Get(ctx, 1)
	require.Error(t, e)
}
func TestDiagnosticsCacheResetDoesNotReuseGeneration(t *testing.T) {
	c, r := newTestDiagnosticsCache(t)
	ctx := context.Background()
	old, e := c.Reserve(ctx, 1, "fp")
	require.NoError(t, e)
	r.FlushAll()
	newer, e := c.Reserve(ctx, 1, "fp")
	require.NoError(t, e)
	require.NotEqual(t, old, newer)
	ok, e := c.CommitIfLatest(ctx, 1, old, "fp", testDiagnosticsReport(t, "old"))
	require.NoError(t, e)
	require.False(t, ok)
}
