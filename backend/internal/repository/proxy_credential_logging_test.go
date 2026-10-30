package repository

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamProxyCredentialLogging(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	username, password := "fictional@user", "fictional:p@ss/word"
	proxyURL := "http://" + url.UserPassword(username, password).String() + "@proxy.invalid:8080"
	cfg := &config.Config{}
	cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationAccountProxy
	cfg.Gateway.OpenAIHTTP2 = config.GatewayOpenAIHTTP2Config{Enabled: true, AllowProxyFallbackToHTTP1: true, FallbackErrorThreshold: 1, FallbackWindowSeconds: 60, FallbackTTLSeconds: 600}
	svc, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	profile := &tlsfingerprint.Profile{Name: "test"}
	entry, err := svc.getClientEntryWithTLS(proxyURL, 701, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	key, _, err := normalizeProxyURL(proxyURL)
	require.NoError(t, err)
	require.Contains(t, key, url.UserPassword(username, password).String())
	require.Equal(t, key, entry.proxyKey)
	cacheKey := "tls:account:701|proxy:" + proxyURL
	require.Same(t, entry, svc.clients[cacheKey])
	again, err := svc.getClientEntryWithTLS(proxyURL, 701, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.Same(t, entry, again)
	entry.poolKey = "stale-test-pool"
	_, err = svc.getClientEntryWithTLS(proxyURL, 701, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	// Reject a literal loopback target before any socket can be opened.
	cfg.Security.URLAllowlist.Enabled = true
	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1/", nil)
	require.NoError(t, err)
	_, err = svc.DoWithTLS(req, proxyURL, 701, 1, profile)
	require.Error(t, err)
	for _, event := range []string{"tls_fingerprint_enabled", "tls_fingerprint_creating_new_client", "tls_fingerprint_reusing_client", "tls_fingerprint_evicting_stale_client"} {
		require.Contains(t, output.String(), event)
	}
	for _, secret := range []string{username, password, url.QueryEscape(username), url.QueryEscape(password)} {
		require.NotContains(t, output.String(), secret)
	}
}

func TestHTTP2FallbackProxyCredentialLogging(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cfg := &config.Config{}
	cfg.Gateway.OpenAIHTTP2 = config.GatewayOpenAIHTTP2Config{Enabled: true, AllowProxyFallbackToHTTP1: true, FallbackErrorThreshold: 1, FallbackWindowSeconds: 60, FallbackTTLSeconds: 600}
	svc, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	key := "http://fictional%40user:fictional%3Apass@proxy.invalid:8080"
	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, key, errors.New("http2: protocol error"))
	require.True(t, svc.isOpenAIHTTP2FallbackActive(key))
	require.False(t, svc.isOpenAIHTTP2FallbackActive("http://proxy.invalid:8080"))
	require.Contains(t, output.String(), "openai_http2_proxy_fallback_activated")
	require.NotContains(t, output.String(), "fictional")
}
