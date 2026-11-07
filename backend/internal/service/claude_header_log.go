package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Correlated inbound/outbound header evidence for Claude traffic (#144). Inbound headers
// are logged once per client request, the first time an Anthropic account request is
// prepared for it; every prepared outbound attempt is logged at the HTTP upstream
// boundary. Both carry client_request_id (or an operation id for background calls).
// Bodies are never logged; credential values never appear in full.

const (
	claudeHeaderLogMessage       = "claude.headers"
	claudeHeaderLogMaxHeaders    = 128
	claudeHeaderLogMaxValueBytes = 2048
	claudeHeaderLogMaxTotalBytes = 32 << 10
	// Secrets at least this long (after the auth scheme) log their last 8 characters.
	claudeSecretTailMinLen = 24
)

var claudeHeaderLogEnabled atomic.Bool

func init() { claudeHeaderLogEnabled.Store(true) }

// SetClaudeHeaderLogEnabled is the volume escape hatch (gateway.log_claude_headers).
func SetClaudeHeaderLogEnabled(enabled bool) {
	claudeHeaderLogEnabled.Store(enabled)
	if !enabled {
		logger.L().Warn("claude header logging disabled by configuration", zap.String("setting", "gateway.log_claude_headers"))
	}
}

type claudeOutboundLogKey struct{}

type claudeOutboundLogInfo struct {
	accountID int64
	operation string
	opID      string
	attempts  *atomic.Int32
	// strippedBetas holds the no-credit constants removed from this attempt's header (#147).
	strippedBetas []string
}

const (
	claudeInboundLoggedKey   = "claude_header_log_inbound_logged"
	claudeOutboundCounterKey = "claude_header_log_attempts"
)

// markClaudeOutbound tags an Anthropic account request for outbound header logging and
// logs the client's inbound headers once per request. c is nil for background calls.
func markClaudeOutbound(c *gin.Context, req *http.Request, account *Account, operation string) *http.Request {
	if req == nil || !claudeHeaderLogEnabled.Load() {
		return req
	}
	info := &claudeOutboundLogInfo{operation: operation}
	if account != nil {
		info.accountID = account.ID
	}
	if c != nil && c.Request != nil {
		if v, ok := c.Get(claudeOutboundCounterKey); ok {
			info.attempts, _ = v.(*atomic.Int32)
		}
		if info.attempts == nil {
			info.attempts = &atomic.Int32{}
			c.Set(claudeOutboundCounterKey, info.attempts)
		}
		if _, logged := c.Get(claudeInboundLoggedKey); !logged {
			c.Set(claudeInboundLoggedKey, true)
			lines, truncated := claudeHeaderSnapshot(c.Request.Header)
			logger.FromContext(c.Request.Context()).Info(claudeHeaderLogMessage,
				zap.String("direction", "inbound"), zap.String("method", c.Request.Method),
				zap.String("path", c.Request.URL.Path), zap.Strings("headers", lines), zap.Bool("headers_truncated", truncated))
		}
	} else {
		info.attempts = &atomic.Int32{}
		info.opID = uuid.NewString()
	}
	return req.WithContext(context.WithValue(req.Context(), claudeOutboundLogKey{}, info))
}

// noteStrippedBetas attaches the no-credit removals to a tagged request's outbound log. Each
// build creates its own log info, so removals never carry over to another attempt or account.
// Untagged requests (header logging off) keep no record.
func noteStrippedBetas(req *http.Request, removed []string) *http.Request {
	if req == nil || len(removed) == 0 {
		return req
	}
	if info, _ := req.Context().Value(claudeOutboundLogKey{}).(*claudeOutboundLogInfo); info != nil {
		info.strippedBetas = removed
	}
	return req
}

// claudeHeaderLogUpstream logs prepared outbound headers of tagged requests, then sends.
type claudeHeaderLogUpstream struct{ inner HTTPUpstream }

// WithClaudeHeaderLog wraps the shared HTTP upstream; untagged requests pass untouched.
func WithClaudeHeaderLog(inner HTTPUpstream) HTTPUpstream {
	if inner == nil {
		return nil
	}
	return &claudeHeaderLogUpstream{inner: inner}
}

func (u *claudeHeaderLogUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	logClaudeOutbound(req, accountID)
	return u.inner.Do(req, proxyURL, accountID, concurrency)
}

func (u *claudeHeaderLogUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	logClaudeOutbound(req, accountID)
	return u.inner.DoWithTLS(req, proxyURL, accountID, concurrency, profile)
}

func logClaudeOutbound(req *http.Request, accountID int64) {
	if req == nil || !claudeHeaderLogEnabled.Load() {
		return
	}
	info, _ := req.Context().Value(claudeOutboundLogKey{}).(*claudeOutboundLogInfo)
	if info == nil {
		return
	}
	if info.accountID == 0 {
		info.accountID = accountID
	}
	lines, truncated := claudeHeaderSnapshot(req.Header)
	fields := []zap.Field{
		zap.String("direction", "outbound"), zap.String("snapshot", "prepared"), zap.String("operation", info.operation),
		zap.Int64("account_id", info.accountID), zap.Int32("attempt", info.attempts.Add(1)),
		zap.String("profile_revision", claude.ActiveClientProfile().Revision),
		zap.String("method", req.Method), zap.String("host", req.URL.Host), zap.String("path", req.URL.Path),
		zap.Strings("headers", lines), zap.Bool("headers_truncated", truncated),
	}
	if len(info.strippedBetas) > 0 {
		fields = append(fields, zap.Strings("stripped_betas", info.strippedBetas))
	}
	if info.opID != "" {
		fields = append(fields, zap.String("operation_id", info.opID))
	}
	logger.FromContext(req.Context()).Info(claudeHeaderLogMessage, fields...)
}

// claudeHeaderSnapshot renders "Name: value" lines (one per value, names sorted, original
// casing kept) with credentials redacted, bounded per value, per count and in total.
func claudeHeaderSnapshot(h http.Header) ([]string, bool) {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	out := make([]string, 0, len(names))
	total, truncated := 0, false
	for _, k := range names {
		for _, v := range h[k] {
			if len(out) >= claudeHeaderLogMaxHeaders || total >= claudeHeaderLogMaxTotalBytes {
				return out, true
			}
			v = redactHeaderValueForLog(k, v)
			if len(v) > claudeHeaderLogMaxValueBytes {
				v = v[:claudeHeaderLogMaxValueBytes] + "…[truncated len=" + strconv.Itoa(len(v)) + "]"
				truncated = true
			}
			line := k + ": " + v
			total += len(line)
			out = append(out, line)
		}
	}
	return out, truncated
}

// claudeLoggableTokenRe matches the token formats whose tail may be logged: Anthropic
// OAuth/API keys and sk- style API keys (the gateway's own default). Anything else that
// sits in a secret-bearing header is treated as unknown-format and shows its length only.
var claudeLoggableTokenRe = regexp.MustCompile(`^sk-[A-Za-z0-9_-]{21,}$`)

// redactHeaderValueForLog applies the credential rule to secret-bearing headers and
// returns other values as they are.
func redactHeaderValueForLog(name, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	switch n := strings.ToLower(strings.TrimSpace(name)); {
	case n == "authorization" || n == "proxy-authorization":
		return redactAuthorizationForLog(value)
	case n == "cookie":
		return redactCookieForLog(value)
	case n == "set-cookie":
		return redactedLength(value)
	case n == "x-api-key" || n == "api-key" || n == "x-goog-api-key" || strings.Contains(n, "token") ||
		strings.Contains(n, "secret") || strings.Contains(n, "password") || strings.Contains(n, "passwd") ||
		strings.HasSuffix(n, "-key") || strings.Contains(n, "credential") || strings.Contains(n, "auth"):
		return redactSecretForLog(value)
	}
	return value
}

// redactAuthorizationForLog recognises only the Bearer and Basic schemes. An unknown
// scheme is not echoed: the whole value is redacted.
func redactAuthorizationForLog(value string) string {
	scheme, rest, hasScheme := strings.Cut(value, " ")
	if hasScheme {
		rest = strings.TrimSpace(rest)
		switch strings.ToLower(scheme) {
		case "bearer":
			return "Bearer " + redactSecretForLog(rest)
		case "basic":
			return "Basic " + redactedLength(rest)
		}
		return redactedLength(value)
	}
	return redactSecretForLog(value)
}

// redactSecretForLog shows last 8 characters, length and a 12-hex SHA-256 prefix only for
// recognised long token formats; every other value shows its length only.
func redactSecretForLog(value string) string {
	if !claudeLoggableTokenRe.MatchString(value) || len(value) < claudeSecretTailMinLen {
		return redactedLength(value)
	}
	sum := sha256.Sum256([]byte(value))
	return "…" + value[len(value)-8:] + " (len=" + strconv.Itoa(len(value)) + " sha256=" + hex.EncodeToString(sum[:])[:12] + ")"
}

func redactedLength(value string) string {
	return "[redacted len=" + strconv.Itoa(len(value)) + "]"
}

// redactCookieForLog keeps cookie names and redacts each value; anything malformed is
// redacted whole.
func redactCookieForLog(value string) string {
	parts := strings.Split(value, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, v, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t,") {
			return "[redacted cookie len=" + strconv.Itoa(len(value)) + "]"
		}
		out = append(out, name+"="+redactSecretForLog(strings.TrimSpace(v)))
	}
	return strings.Join(out, "; ")
}
