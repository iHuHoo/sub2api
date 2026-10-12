package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const diagnosticsTTLSeconds = 24 * 60 * 60

type proxyDiagnosticsCache struct{ rdb *redis.Client }

func NewProxyDiagnosticsCache(rdb *redis.Client) service.ProxyDiagnosticsCache {
	return &proxyDiagnosticsCache{rdb}
}
func diagnosticsKeys(id int64) []string {
	prefix := fmt.Sprintf("proxy:diagnostics:{%d}:", id)
	return []string{prefix + "reservation", prefix + "report"}
}

// A nonce prevents reuse after Redis expiry, eviction or restore. The latest reservation wins.
var diagnosticsReserve = redis.NewScript(`
redis.call('HSET', KEYS[1], 'sequence', ARGV[3], 'fingerprint', ARGV[1])
redis.call('EXPIRE', KEYS[1], ARGV[2])
return ARGV[3]
`)
var diagnosticsCommit = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'sequence') ~= ARGV[1] or
   redis.call('HGET', KEYS[1], 'fingerprint') ~= ARGV[2] then return 0 end
redis.call('SET', KEYS[2], ARGV[3], 'EX', ARGV[4])
return 1
`)

func (c *proxyDiagnosticsCache) Reserve(ctx context.Context, id int64, fp string) (string, error) {
	keys := diagnosticsKeys(id)
	return diagnosticsReserve.Run(ctx, c.rdb, keys[:1], fp, diagnosticsTTLSeconds, uuid.NewString()).Text()
}
func (c *proxyDiagnosticsCache) CommitIfLatest(ctx context.Context, id int64, seq, fp string, report json.RawMessage) (bool, error) {
	payload, e := json.Marshal(service.CachedDiagnosticsReport{Fingerprint: fp, Sequence: seq, Report: report})
	if e != nil {
		return false, e
	}
	keys := diagnosticsKeys(id)
	n, e := diagnosticsCommit.Run(ctx, c.rdb, keys, seq, fp, payload, diagnosticsTTLSeconds).Int64()
	return n == 1, e
}
func (c *proxyDiagnosticsCache) Get(ctx context.Context, id int64) (service.CachedDiagnosticsReport, error) {
	var report service.CachedDiagnosticsReport
	b, e := c.rdb.Get(ctx, diagnosticsKeys(id)[1]).Bytes()
	if e != nil {
		return report, e
	}
	e = json.Unmarshal(b, &report)
	return report, e
}
