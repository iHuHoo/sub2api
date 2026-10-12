package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

var (
	ErrDiagnosticsDisabled          = apperrors.New(503, "DIAGNOSTICS_DISABLED", "Proxy diagnostics is disabled")
	ErrDiagnosticsReportNotFound    = apperrors.NotFound("REPORT_NOT_FOUND", "No current diagnostics report")
	ErrDiagnosticsProxyChanged      = apperrors.Conflict("PROXY_CHANGED", "Proxy configuration changed during check")
	ErrDiagnosticsUnavailable       = apperrors.New(503, "service_unavailable", "Diagnostics service unavailable")
	ErrDiagnosticsSchemaUnsupported = apperrors.New(503, "schema_unsupported", "Unsupported diagnostics report schema")
)

type DiagnosticsResponse struct {
	Report json.RawMessage `json:"report"`
	Saved  bool            `json:"saved"`
}
type CachedDiagnosticsReport struct {
	Fingerprint string          `json:"fingerprint"`
	Sequence    string          `json:"sequence"`
	Report      json.RawMessage `json:"report"`
}
type ProxyDiagnosticsCache interface {
	Reserve(context.Context, int64, string) (string, error)
	CommitIfLatest(context.Context, int64, string, string, json.RawMessage) (bool, error)
	Get(context.Context, int64) (CachedDiagnosticsReport, error)
}
type ProxyDiagnosticsService struct {
	repo            ProxyRepository
	cache           ProxyDiagnosticsCache
	client          *http.Client
	endpoint, token string
	hmacSecret      []byte
}
type diagnosticsProxy struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func diagnosticsProxyFrom(p *Proxy) diagnosticsProxy {
	return diagnosticsProxy{strings.ToLower(p.Protocol), strings.ToLower(p.Host), p.Port, p.Username, p.Password}
}
func NewProxyDiagnosticsService(repo ProxyRepository, cache ProxyDiagnosticsCache, cfg *config.Config) (*ProxyDiagnosticsService, error) {
	s := &ProxyDiagnosticsService{repo: repo, cache: cache}
	c := cfg.Subproxy
	if !c.Enabled {
		return s, nil
	}
	if e := c.Validate(); e != nil {
		return nil, e
	}
	token, e := c.Token.Read()
	if e != nil {
		return nil, e
	}
	secret, e := c.HMACSecret.Read()
	if e != nil {
		return nil, e
	}
	if len(secret) < 32 || secret == token {
		return nil, errors.New("subproxy: independent HMAC secret of at least 32 bytes required")
	}
	s.token = token
	s.hmacSecret = []byte(secret)
	s.endpoint = strings.TrimRight(c.BaseURL, "/") + "/v1/checks"
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("subproxy: unsupported default transport")
	}
	tr := transport.Clone()
	tr.Proxy = nil // Internal credentials must not traverse an environment proxy.
	s.client = &http.Client{Transport: tr, Timeout: 65 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return s, nil
}
func (s *ProxyDiagnosticsService) Enabled() bool { return s != nil && s.client != nil }

// Classifications are fixed local labels; never log upstream errors or proxy configuration.
func warnDiagnosticsFailure(id int64, classification string) {
	slog.Warn("proxy_diagnostics_failed", "proxy_id", id, "classification", classification)
}
func (s *ProxyDiagnosticsService) fingerprint(p *Proxy) string {
	// Fixed struct ordering is the canonical serialization; include token to invalidate on rotation.
	b, _ := json.Marshal(struct {
		Proxy diagnosticsProxy
		Token string
	}{diagnosticsProxyFrom(p), s.token})
	h := hmac.New(sha256.New, s.hmacSecret)
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *ProxyDiagnosticsService) Check(ctx context.Context, id int64, mode string) (DiagnosticsResponse, error) {
	var result DiagnosticsResponse
	if !s.Enabled() {
		slog.Debug("proxy_diagnostics_disabled", "proxy_id", id, "classification", "diagnostics_disabled")
		return result, ErrDiagnosticsDisabled
	}
	if mode != "quick" && mode != "sample" {
		warnDiagnosticsFailure(id, "invalid_mode")
		return result, apperrors.BadRequest("INVALID_MODE", "Mode must be quick or sample")
	}
	p, e := s.repo.GetByID(ctx, id)
	if e != nil {
		warnDiagnosticsFailure(id, "proxy_read_failed")
		return result, e
	}
	fingerprint := s.fingerprint(p)
	var sequence string
	if s.cache != nil {
		if seq, err := s.cache.Reserve(ctx, id, fingerprint); err == nil {
			sequence = seq
		} else {
			warnDiagnosticsFailure(id, "cache_reserve_failed")
		}
	} else {
		warnDiagnosticsFailure(id, "cache_unavailable")
	}
	requestID := uuid.NewString()
	body, _ := json.Marshal(struct {
		RequestID string           `json:"request_id"`
		Mode      string           `json:"mode"`
		Proxy     diagnosticsProxy `json:"proxy"`
	}{requestID, mode, diagnosticsProxyFrom(p)})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if e != nil {
		warnDiagnosticsFailure(id, "request_build_failed")
		return result, ErrDiagnosticsUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, e := s.client.Do(req)
	if e != nil {
		warnDiagnosticsFailure(id, "service_unavailable")
		return result, ErrDiagnosticsUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if e != nil || len(raw) > 256*1024 {
		warnDiagnosticsFailure(id, "response_read_failed")
		return result, ErrDiagnosticsUnavailable
	}
	if resp.StatusCode != 200 {
		switch resp.StatusCode {
		case 401:
			warnDiagnosticsFailure(id, "service_auth_failed")
			return result, apperrors.New(503, "service_auth_failed", "Diagnostics service authentication failed")
		case 429:
			warnDiagnosticsFailure(id, "busy")
			return result, apperrors.New(429, "busy", "Diagnostics service is busy")
		case 400:
			var remote struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &remote) == nil {
				switch remote.Error.Code {
				case "proxy_address_unsupported":
					warnDiagnosticsFailure(id, "proxy_address_unsupported")
					return result, apperrors.BadRequest("proxy_address_unsupported", "Proxy address is unsupported by diagnostics policy")
				case "request_invalid":
					warnDiagnosticsFailure(id, "request_invalid")
					return result, apperrors.BadRequest("request_invalid", "Proxy configuration is invalid for diagnostics")
				}
			}
		}
		warnDiagnosticsFailure(id, "service_unavailable")
		return result, ErrDiagnosticsUnavailable
	}
	report, e := parseDiagnosticsReport(raw)
	if e != nil {
		if errors.Is(e, ErrDiagnosticsSchemaUnsupported) {
			warnDiagnosticsFailure(id, "schema_unsupported")
			return result, ErrDiagnosticsSchemaUnsupported
		}
		warnDiagnosticsFailure(id, "report_invalid")
		return result, ErrDiagnosticsUnavailable
	}
	var identity struct {
		RequestID string `json:"request_id"`
		Mode      string `json:"mode"`
	}
	_ = json.Unmarshal(report, &identity)
	if identity.RequestID != requestID || identity.Mode != mode {
		warnDiagnosticsFailure(id, "report_identity_mismatch")
		return result, ErrDiagnosticsUnavailable
	}
	current, e := s.repo.GetByID(ctx, id)
	if e != nil {
		warnDiagnosticsFailure(id, "proxy_read_failed")
		return result, e
	}
	if s.fingerprint(current) != fingerprint {
		warnDiagnosticsFailure(id, "proxy_changed")
		return result, ErrDiagnosticsProxyChanged
	}
	result.Report = report
	if sequence != "" {
		result.Saved, e = s.cache.CommitIfLatest(ctx, id, sequence, fingerprint, report)
		if e != nil {
			result.Saved = false
			warnDiagnosticsFailure(id, "cache_commit_failed")
		} else if !result.Saved {
			warnDiagnosticsFailure(id, "cache_superseded")
		}
	}
	return result, nil
}
func (s *ProxyDiagnosticsService) Latest(ctx context.Context, id int64) (DiagnosticsResponse, error) {
	if !s.Enabled() {
		slog.Debug("proxy_diagnostics_disabled", "proxy_id", id, "classification", "diagnostics_disabled")
		return DiagnosticsResponse{}, ErrDiagnosticsDisabled
	}
	p, e := s.repo.GetByID(ctx, id)
	if e != nil {
		warnDiagnosticsFailure(id, "proxy_read_failed")
		return DiagnosticsResponse{}, e
	}
	if s.cache == nil {
		warnDiagnosticsFailure(id, "cache_unavailable")
		return DiagnosticsResponse{}, ErrDiagnosticsReportNotFound
	}
	cached, e := s.cache.Get(ctx, id)
	if e != nil {
		warnDiagnosticsFailure(id, "cache_read_failed")
		return DiagnosticsResponse{}, ErrDiagnosticsReportNotFound
	}
	if cached.Fingerprint != s.fingerprint(p) {
		warnDiagnosticsFailure(id, "report_stale")
		return DiagnosticsResponse{}, ErrDiagnosticsReportNotFound
	}
	report, e := parseDiagnosticsReport(cached.Report)
	if e != nil {
		if errors.Is(e, ErrDiagnosticsSchemaUnsupported) {
			warnDiagnosticsFailure(id, "schema_unsupported")
			return DiagnosticsResponse{}, ErrDiagnosticsSchemaUnsupported
		}
		warnDiagnosticsFailure(id, "report_invalid")
		return DiagnosticsResponse{}, ErrDiagnosticsReportNotFound
	}
	return DiagnosticsResponse{Report: report, Saved: true}, nil
}
