package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type stubIdentityCache struct {
	fingerprint *Fingerprint
	setCalls    int
	lastSet     *Fingerprint
}

func (s *stubIdentityCache) GetFingerprint(_ context.Context, _ int64) (*Fingerprint, error) {
	if s.fingerprint == nil {
		return nil, nil
	}
	clone := *s.fingerprint
	return &clone, nil
}

func (s *stubIdentityCache) SetFingerprint(_ context.Context, _ int64, fp *Fingerprint) error {
	s.setCalls++
	clone := *fp
	s.lastSet = &clone
	s.fingerprint = &clone
	return nil
}

func (s *stubIdentityCache) GetMaskedSessionID(_ context.Context, _ int64) (string, error) {
	return "", nil
}

func (s *stubIdentityCache) SetMaskedSessionID(_ context.Context, _ int64, _ string) error {
	return nil
}

func headersWithUA(ua string) http.Header {
	h := http.Header{}
	if ua != "" {
		h.Set("User-Agent", ua)
	}
	return h
}

// withTestClientProfile installs a distinct valid profile so assertions prove values
// come from the profile, and restores the previous one afterwards.
func withTestClientProfile(t *testing.T) claude.ClientProfile {
	t.Helper()
	prev := claude.ActiveClientProfile()
	p := prev
	p.Revision, p.CLIVersion, p.UserAgent = "test.1", "2.1.999", "claude-cli/2.1.999 (external, cli)"
	p.StainlessPackageVersion, p.StainlessRuntimeVersion = "0.777.0", "v99.1.2"
	require.NoError(t, claude.SetActiveClientProfile(p))
	t.Cleanup(func() { require.NoError(t, claude.SetActiveClientProfile(prev)) })
	return p
}

// adversarialClaudeHeaders are client values for every profile-owned header, in mixed
// casing, that must never reach an OAuth upstream.
var adversarialClaudeHeaders = map[string]string{
	"user-agent": "claude-cli/2.1.400 (external, cli)", "X-Stainless-Lang": "py", "x-stainless-package-version": "0.200.0",
	"X-Stainless-OS": "Windows", "x-stainless-arch": "x64", "X-Stainless-Runtime": "deno", "x-stainless-runtime-version": "v30.0.0",
	"x-stainless-timeout": "999", "X-App": "desktop", "anthropic-dangerous-direct-browser-access": "false", "Anthropic-Version": "2099-01-01",
}

func requireProfileHeaders(t *testing.T, p claude.ClientProfile, h http.Header) {
	t.Helper()
	want := map[string]string{
		"User-Agent": p.UserAgent, "X-Stainless-Lang": p.StainlessLang, "X-Stainless-Package-Version": p.StainlessPackageVersion,
		"X-Stainless-OS": p.StainlessOS, "X-Stainless-Arch": p.StainlessArch, "X-Stainless-Runtime": p.StainlessRuntime,
		"X-Stainless-Runtime-Version": p.StainlessRuntimeVersion, "X-Stainless-Timeout": p.StainlessTimeout, "X-App": p.XApp,
		"Anthropic-Dangerous-Direct-Browser-Access": p.DangerousDirectBrowserAccess, "anthropic-version": p.AnthropicVersion,
	}
	require.Len(t, want, len(claudeClientProfileHeaderNames), "every profile-owned header is asserted")
	for k, v := range want {
		var got []string
		for name, values := range h {
			if strings.EqualFold(name, k) {
				got = append(got, values...)
			}
		}
		require.Equal(t, []string{v}, got, "%s: exactly one value, from the profile", k)
	}
}

type failingIdentityCache struct{ stubIdentityCache }

func (f *failingIdentityCache) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	return nil, errors.New("redis down")
}

func (f *failingIdentityCache) SetFingerprint(context.Context, int64, *Fingerprint) error {
	return errors.New("redis down")
}

// Every OAuth route (mimic and native, messages and count_tokens, default and custom base
// URL), with fingerprint unification on or off and with a failing cache, sends exactly
// the profile's headers and a matching cc_version, whatever the client sent.
func TestClaudeOAuthRequestsUseOneClientProfile(t *testing.T) {
	p := withTestClientProfile(t)
	stale := &Fingerprint{ClientID: "client-1", UserAgent: "claude-cli/2.1.316 (external, claude-desktop-3p, agent-sdk/0.3.308)",
		StainlessPackageVersion: "0.128.0", StainlessRuntimeVersion: "v26.3.0", StainlessOS: "Windows", StainlessArch: "x64"}
	type variant struct {
		name      string
		cache     IdentityCache
		disableFP bool
		customURL bool
	}
	variants := []variant{
		{name: "stale-cache", cache: &stubIdentityCache{fingerprint: stale}},
		{name: "fp-disabled", cache: &stubIdentityCache{fingerprint: stale}, disableFP: true},
		{name: "cache-failure", cache: &failingIdentityCache{}},
		{name: "no-identity-service"},
		{name: "custom-base-url", cache: &stubIdentityCache{fingerprint: stale}, customURL: true},
	}
	for _, v := range variants {
		for _, mimic := range []bool{true, false} {
			for _, endpoint := range []string{"messages", "count_tokens"} {
				t.Run(v.name+"/"+endpoint+map[bool]string{true: "/mimic", false: "/native"}[mimic], func(t *testing.T) {
					resetGatewayForwardingSettingsCacheForTest(t)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					for k, val := range adversarialClaudeHeaders {
						c.Request.Header[k] = []string{val}
					}
					body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
					billing, err := buildBillingAttributionText(body, "2.1.400")
					require.NoError(t, err)
					body, err = sjson.SetBytes(body, "system.0.text", billing)
					require.NoError(t, err)

					cfg := &config.Config{}
					svc := &GatewayService{cfg: cfg}
					if v.cache != nil {
						svc.identityService = NewIdentityService(v.cache)
					}
					if v.disableFP {
						svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
							SettingKeyEnableFingerprintUnification: "false",
						}}, cfg)
					}
					account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
					if v.customURL {
						account.Extra = map[string]any{"custom_base_url_enabled": true, "custom_base_url": "https://relay.example.com"}
					}
					var req *http.Request
					var wire []byte
					if endpoint == "messages" {
						req, wire, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "tok", "oauth", "claude-haiku-4-5", false, mimic)
					} else {
						req, wire, err = svc.buildCountTokensRequest(context.Background(), c, account, body, "tok", "oauth", "claude-haiku-4-5", mimic)
					}
					require.NoError(t, err)
					if v.customURL {
						require.Equal(t, "relay.example.com", req.URL.Host, "custom base OAuth stays in the OAuth identity row")
					}
					requireProfileHeaders(t, p, req.Header)
					text := gjson.GetBytes(wire, "system.0.text").String()
					require.Contains(t, text, "cc_version="+p.CLIVersion+"."+computeClaudeCodeFingerprint(wire, p.CLIVersion)+";")
					require.NotContains(t, text, "2.1.400")
				})
			}
		}
	}
}

// API-key accounts keep their pass-through contract: client values are not replaced.
func TestClaudeClientProfileDoesNotTouchAPIKeyRequests(t *testing.T) {
	withTestClientProfile(t)
	resetGatewayForwardingSettingsCacheForTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "my-app/1.0")
	c.Request.Header.Set("Anthropic-Version", "2099-01-01")
	svc := &GatewayService{cfg: &config.Config{}}
	account := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}}
	req, _, err := svc.buildUpstreamRequest(context.Background(), c, account,
		[]byte(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}]}`), "k", "apikey", "claude-haiku-4-5", false, false)
	require.NoError(t, err)
	require.Equal(t, "my-app/1.0", getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, "2099-01-01", getHeaderRaw(req.Header, "anthropic-version"))
}

// The cache keeps only the stable ClientID; software fields align to the profile and
// client headers never change them.
func TestGetOrCreateFingerprintUsesProfileAndKeepsClientID(t *testing.T) {
	p := withTestClientProfile(t)
	cache := &stubIdentityCache{fingerprint: &Fingerprint{ClientID: "keep-me", UserAgent: "claude-cli/2.1.316 (external, cli)",
		StainlessPackageVersion: "0.94.0", StainlessOS: "MacOS", StainlessArch: "arm64", UpdatedAt: 1}}
	svc := NewIdentityService(cache)
	fp, err := svc.GetOrCreateFingerprint(context.Background(), 7, headersWithUA("claude-cli/2.1.500 (external, cli)"))
	require.NoError(t, err)
	require.Equal(t, "keep-me", fp.ClientID)
	require.Equal(t, p.UserAgent, fp.UserAgent)
	require.Equal(t, p.StainlessPackageVersion, fp.StainlessPackageVersion)
	require.Equal(t, p.StainlessOS, fp.StainlessOS)
	require.Equal(t, 1, cache.setCalls, "aligned once and persisted")
	require.Equal(t, "keep-me", cache.lastSet.ClientID)

	_, err = svc.GetOrCreateFingerprint(context.Background(), 7, headersWithUA("curl/8.7.1"))
	require.NoError(t, err)
	require.Equal(t, 1, cache.setCalls, "already aligned and fresh: no write")

	empty := &stubIdentityCache{}
	fresh, err := NewIdentityService(empty).GetOrCreateFingerprint(context.Background(), 8, headersWithUA("curl/8.7.1"))
	require.NoError(t, err)
	require.NotEmpty(t, fresh.ClientID)
	require.Equal(t, p.UserAgent, fresh.UserAgent)
}

// Captured 2.1.318 suffixes: the CLI fingerprints the first user text that is not a
// <system-reminder> (41c for "say ok", 861 for the tool prompt). Session seeds still use
// the first block, so existing session ids do not move.
func TestClaudeCodeFingerprintSkipsSystemReminders(t *testing.T) {
	body := func(prompt string) []byte {
		b, err := sjson.SetBytes([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\n# Environment\nYou have been invoked in the following environment"},{"type":"text","text":"<system-reminder>\nToday's date is 2026-11-05.\n</system-reminder>"}]}]}`),
			"messages.0.content.-1", map[string]string{"type": "text", "text": prompt})
		require.NoError(t, err)
		return b
	}
	require.Equal(t, "41c", computeClaudeCodeFingerprint(body("say ok"), "2.1.318"))
	require.Equal(t, "861", computeClaudeCodeFingerprint(body("please run the command: echo hi"), "2.1.318"))
	require.True(t, strings.HasPrefix(extractFirstUserText(body("say ok")), "<system-reminder>"), "session seed input unchanged")
	require.Equal(t, "hello", extractFingerprintUserText([]byte(`{"messages":[{"role":"user","content":"hello"}]}`)))
	onlyReminder := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>"}]}]}`)
	require.Equal(t, "<system-reminder>x</system-reminder>", extractFingerprintUserText(onlyReminder))
}
