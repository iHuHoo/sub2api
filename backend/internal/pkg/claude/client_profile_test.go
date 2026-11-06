package claude

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const profileFile = "../../../resources/claude-client-profile/profile.json"

func TestMain(m *testing.M) {
	p, err := LoadClientProfileFile(profileFile)
	if err != nil {
		panic(err)
	}
	if err := SetActiveClientProfile(p); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// Without a loaded profile there is no identity to send.
func TestActiveClientProfileHasNoFallback(t *testing.T) {
	prev := activeClientProfile.Load()
	activeClientProfile.Store(nil)
	defer activeClientProfile.Store(prev)
	require.PanicsWithValue(t, "claude client profile not loaded: call SetActiveClientProfile at startup (gateway.claude_client_profile_file)", func() { ActiveClientProfile() })
}

// The profile's evidence digest is the checked-in capture, and every software field
// equals what the captured interactive 2.1.318 CLI actually sent.
func TestClientProfileMatchesCaptureEvidence(t *testing.T) {
	p := ActiveClientProfile()
	raw, err := os.ReadFile("testdata/client_profile_capture/evidence-" + p.CLIVersion + ".json")
	require.NoError(t, err)
	require.Equal(t, p.Source.EvidenceSHA256, sha256Hex(raw))

	var ev struct {
		CLIVersion string `json:"cli_version"`
		Cases      map[string][]struct {
			Headers      [][2]string `json:"headers"`
			BillingBlock string      `json:"billing_block"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &ev))
	require.Equal(t, p.CLIVersion, ev.CLIVersion)
	want := map[string]string{
		"user-agent": p.UserAgent, "anthropic-version": p.AnthropicVersion, "x-app": p.XApp,
		"x-stainless-lang": p.StainlessLang, "x-stainless-package-version": p.StainlessPackageVersion,
		"x-stainless-os": p.StainlessOS, "x-stainless-arch": p.StainlessArch, "x-stainless-runtime": p.StainlessRuntime,
		"x-stainless-runtime-version": p.StainlessRuntimeVersion, "x-stainless-timeout": p.StainlessTimeout,
		"anthropic-dangerous-direct-browser-access": p.DangerousDirectBrowserAccess,
	}
	for _, name := range []string{"interactive_sonnet46", "interactive_opus55", "interactive_haiku55"} {
		reqs := ev.Cases[name]
		require.NotEmpty(t, reqs, name)
		for _, r := range reqs {
			got := map[string]string{}
			for _, h := range r.Headers {
				got[strings.ToLower(h[0])] = h[1]
			}
			for k, v := range want {
				require.Equal(t, v, got[k], "%s %s", name, k)
			}
			require.Contains(t, r.BillingBlock, "cc_version="+p.CLIVersion+".")
			require.Contains(t, r.BillingBlock, "cc_entrypoint=cli;")
		}
	}
	// Headers come from one profile snapshot.
	h := DefaultHeaders()
	require.Equal(t, p.UserAgent, h["User-Agent"])
	require.Equal(t, p.StainlessPackageVersion, h["X-Stainless-Package-Version"])
	require.Equal(t, p.StainlessRuntimeVersion, h["X-Stainless-Runtime-Version"])
}

func TestParseClientProfileRejectsBadInput(t *testing.T) {
	good, err := os.ReadFile(profileFile)
	require.NoError(t, err)
	_, err = ParseClientProfile(good)
	require.NoError(t, err)

	source := func(m map[string]any) map[string]any {
		src, ok := m["source"].(map[string]any)
		require.True(t, ok)
		return src
	}
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		require.NoError(t, json.Unmarshal(good, &m))
		f(m)
		b, _ := json.Marshal(m)
		return b
	}
	for name, tc := range map[string]struct {
		raw  []byte
		want string
	}{
		"unknown field":      {mutate(func(m map[string]any) { m["authorization"] = "Bearer x" }), "unknown field"},
		"missing field":      {mutate(func(m map[string]any) { delete(m, "x_stainless_package_version") }), "x_stainless_package_version is required"},
		"ua mismatch":        {mutate(func(m map[string]any) { m["user_agent"] = "claude-cli/2.1.300 (external, cli)" }), "user_agent must be"},
		"bad version":        {mutate(func(m map[string]any) { m["cli_version"] = "2.1"; m["user_agent"] = "claude-cli/2.1 (external, cli)" }), "cli_version"},
		"header injection":   {mutate(func(m map[string]any) { m["x_app"] = "cli\r\nX-Evil: 1" }), "control characters"},
		"schema":             {mutate(func(m map[string]any) { m["schema_version"] = 3 }), "schema_version"},
		"bad digest":         {mutate(func(m map[string]any) { source(m)["artifact_digest"] = "abc" }), "artifact_digest"},
		"duplicate key":      {[]byte(`{"schema_version":1,"schema_version":1}`), "duplicate key"},
		"trailing data":      {append(append([]byte{}, good...), []byte(" {}")...), "trailing data"},
		"empty":              {[]byte(``), "decode"},
		"not an object":      {[]byte(`[]`), "decode"},
		"unknown source key": {mutate(func(m map[string]any) { source(m)["token"] = "x" }), "unknown field"},
	} {
		_, err := ParseClientProfile(tc.raw)
		require.ErrorContains(t, err, tc.want, name)
	}
	_, err = LoadClientProfileFile("does-not-exist.json")
	require.ErrorContains(t, err, "read")
	_, err = LoadClientProfileFile(" ")
	require.ErrorContains(t, err, "empty")
	require.Error(t, SetActiveClientProfile(ClientProfile{}), "invalid profiles are never installed")
}
