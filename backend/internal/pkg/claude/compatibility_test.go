package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type compatibilityFixture struct {
	Version    string          `json:"version"`
	Endpoint   string          `json:"endpoint"`
	Entrypoint string          `json:"entrypoint"`
	Model      string          `json:"model"`
	URL        string          `json:"url"`
	Headers    [][2]string     `json:"headers"`
	Body       json.RawMessage `json:"body"`
}

func loadCompatibilityFixtures(t *testing.T) []compatibilityFixture {
	t.Helper()
	files, err := filepath.Glob("testdata/compatibility/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	var out []compatibilityFixture
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var fx compatibilityFixture
		require.NoError(t, json.Unmarshal(raw, &fx), f)
		out = append(out, fx)
	}
	return out
}

func fixtureHeader(fx compatibilityFixture, name string) (string, bool) {
	for _, h := range fx.Headers {
		if strings.EqualFold(h[0], name) {
			return h[1], true
		}
	}
	return "", false
}

// Every shipped row is a sanitized capture, and every capture is a row with the same betas
// in the same wire order: nothing extrapolated, nothing missing.
func TestShippedCompatibilityMatchesCaptures(t *testing.T) {
	p := schema2Profile(t)
	require.True(t, p.HasCompatibility())
	fixtures := loadCompatibilityFixtures(t)
	require.Equal(t, len(fixtures), p.CompatibilityRowCount(), "one row per captured request shape")
	for _, fx := range fixtures {
		name := fx.Version + " " + fx.Endpoint + " " + fx.Entrypoint + " " + fx.Model
		row, ok := p.CompatibilityRow(fx.Version, fx.Endpoint, fx.Entrypoint, fx.Model)
		require.True(t, ok, name)
		beta, _ := fixtureHeader(fx, "anthropic-beta")
		require.Equal(t, strings.Split(beta, ","), row.DefaultBetas, name)

		ua, _ := fixtureHeader(fx, "user-agent")
		suffix := map[string]string{CompatibilityEntrypointCLI: "(external, cli)", CompatibilityEntrypointSDK: "(external, sdk-cli)"}[fx.Entrypoint]
		require.Equal(t, "claude-cli/"+fx.Version+" "+suffix, ua, name)

		_, hasTimeout := fixtureHeader(fx, "x-stainless-timeout")
		require.Equal(t, fx.Endpoint == CompatibilityEndpointMessages, hasTimeout, "the CLI sends X-Stainless-Timeout on messages only: %s", name)
		for _, secret := range []string{"authorization", "x-claude-code-session-id"} {
			_, has := fixtureHeader(fx, secret)
			require.False(t, has, "%s keeps no %s", name, secret)
		}

		// A condition the body meets is listed, and its beta was actually sent.
		for cname, cond := range compatibilityConditions {
			if cond.Match(fx.Body) {
				require.Contains(t, row.Requires, cname, name)
				require.Contains(t, row.DefaultBetas, cond.Beta, name)
			}
		}
	}
}

// Lookups are exact: no nearest version, model family or other entrypoint.
func TestCompatibilityLookupIsExact(t *testing.T) {
	p := schema2Profile(t)
	_, ok := p.CompatibilityRow("2.1.318", CompatibilityEndpointMessages, CompatibilityEntrypointCLI, "claude-sonnet-4-6")
	require.True(t, ok)
	for _, miss := range [][4]string{
		{"2.1.317", "messages", "cli", "claude-sonnet-4-6"},
		{"2.1.280", "messages", "cli", "claude-sonnet-4-6"},
		{"2.1.318", "messages", "cli", "claude-sonnet-4"},
		{"2.1.318", "messages", "cli", "claude-sonnet-4-6-20991231"},
		{"2.1.318", "messages", "sdk", "claude-haiku-4-5"},
		{"2.1.318", "count_tokens", "cli", "claude-haiku-4-5"},
		{"2.1.318", "count_tokens", "sdk", "claude-sonnet-4-6"},
	} {
		_, ok := p.CompatibilityRow(miss[0], miss[1], miss[2], miss[3])
		require.False(t, ok, "%v", miss)
	}
	legacy := legacyProfile(t)
	_, ok = legacy.CompatibilityRow("2.1.318", "messages", "cli", "claude-sonnet-4-6")
	require.False(t, ok)
}

// Callers cannot change the table: not through a returned row, a profile copy, or the
// bytes the profile was parsed from.
func TestCompatibilityTableIsImmutable(t *testing.T) {
	raw, err := os.ReadFile(schema2File)
	require.NoError(t, err)
	p, err := ParseClientProfile(raw)
	require.NoError(t, err)
	want, ok := p.CompatibilityRow("2.1.318", "messages", "cli", "claude-sonnet-4-6")
	require.True(t, ok)
	want = cloneRow(want)

	got, _ := p.CompatibilityRow("2.1.318", "messages", "cli", "claude-sonnet-4-6")
	got.DefaultBetas[0], got.Requires[0] = "mutated", "mutated"
	got.DefaultBetas = append(got.DefaultBetas, "extra")
	copied := p
	copied.CLIVersion = "9.9.9"
	for i := range raw {
		raw[i] = ' '
	}
	again, _ := p.CompatibilityRow("2.1.318", "messages", "cli", "claude-sonnet-4-6")
	require.Equal(t, want, again)
	viaCopy, _ := copied.CompatibilityRow("2.1.318", "messages", "cli", "claude-sonnet-4-6")
	require.Equal(t, want, viaCopy)
}

func cloneRow(r CompatibilityRow) CompatibilityRow {
	r.DefaultBetas, r.OptionalBetas, r.Requires = slices.Clone(r.DefaultBetas), slices.Clone(r.OptionalBetas), slices.Clone(r.Requires)
	return r
}

func legacyProfile(t *testing.T) ClientProfile {
	t.Helper()
	raw, err := os.ReadFile("testdata/profile-schema1.json")
	require.NoError(t, err)
	p, err := ParseClientProfile(raw)
	require.NoError(t, err)
	return p
}

// A schema 1 file (the #144 shipped file or an operator's copy) still loads, with no table;
// the two schemas cannot be mixed.
func TestLegacySchema1ProfileLoadsWithoutCompatibility(t *testing.T) {
	p := legacyProfile(t)
	require.Equal(t, 1, p.SchemaVersion)
	require.False(t, p.HasCompatibility())
	require.Zero(t, p.CompatibilityRowCount())
	require.NoError(t, SetActiveClientProfile(p))
	defer func() { require.NoError(t, SetActiveClientProfile(mustShippedProfile(t))) }()
	require.False(t, ActiveClientProfile().HasCompatibility())

	shipped := schema2ProfileMap(t)
	legacyWithTable := clone(t, shipped)
	legacyWithTable["schema_version"] = 1
	_, err := ParseClientProfile(marshal(t, legacyWithTable))
	require.ErrorContains(t, err, "compatibility requires schema_version 2 or 3")

	schema2NoTable := clone(t, shipped)
	delete(schema2NoTable, "compatibility")
	_, err = ParseClientProfile(marshal(t, schema2NoTable))
	require.ErrorContains(t, err, "schema_version 2 requires compatibility")

	require.ErrorContains(t, SetActiveClientProfile(ClientProfile{SchemaVersion: 2}), "requires compatibility",
		"a hand-built schema 2 profile without a parsed table is never installed")
}

// The schema 2 tests run against the last shipped schema 2 file, which stays loadable.
const schema2File = "testdata/profile-schema2.json"

func schema2Profile(t *testing.T) ClientProfile {
	t.Helper()
	p, err := LoadClientProfileFile(schema2File)
	require.NoError(t, err)
	return p
}

func schema2ProfileMap(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(schema2File)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func mustShippedProfile(t *testing.T) ClientProfile {
	t.Helper()
	p, err := LoadClientProfileFile(profileFile)
	require.NoError(t, err)
	return p
}

func shippedProfileMap(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(profileFile)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func clone(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(marshal(t, m), &out))
	return out
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestParseCompatibilityRejectsBadTables(t *testing.T) {
	type obj = map[string]any
	item := func(list any, i int) obj {
		l, _ := list.([]any)
		o, _ := l[i].(obj)
		return o
	}
	compat := func(m obj) obj {
		o, _ := m["compatibility"].(obj)
		return o
	}
	version := func(m obj, i int) obj { return item(compat(m)["versions"], i) }
	row := func(m obj, v, r int) obj { return item(version(m, v)["rows"], r) }
	for name, tc := range map[string]struct {
		edit func(m obj)
		want string
	}{
		"unknown table key":          {func(m obj) { compat(m)["fallback"] = true }, "unknown field"},
		"unknown row key":            {func(m obj) { row(m, 0, 0)["wildcard"] = true }, "unknown field"},
		"no versions":                {func(m obj) { compat(m)["versions"] = []any{} }, "versions is empty"},
		"v prefix":                   {func(m obj) { version(m, 0)["version"] = "v2.1.318" }, "strict three-part"},
		"two parts":                  {func(m obj) { version(m, 0)["version"] = "2.1" }, "strict three-part"},
		"leading zero":               {func(m obj) { version(m, 0)["version"] = "2.01.318" }, "strict three-part"},
		"prerelease":                 {func(m obj) { version(m, 0)["version"] = "2.1.318-rc.1" }, "strict three-part"},
		"build metadata":             {func(m obj) { version(m, 0)["version"] = "2.1.318+b1" }, "strict three-part"},
		"duplicate version":          {func(m obj) { version(m, 1)["version"] = version(m, 0)["version"] }, "duplicate version"},
		"unknown contract":           {func(m obj) { version(m, 0)["body_contract"] = "anything-goes" }, "not a known contract"},
		"no rows":                    {func(m obj) { version(m, 0)["rows"] = []any{} }, "rows is empty"},
		"bad endpoint":               {func(m obj) { row(m, 0, 0)["endpoint"] = "complete" }, "endpoint"},
		"bad entrypoint":             {func(m obj) { row(m, 0, 0)["entrypoint"] = "sdk-ts" }, "entrypoint"},
		"no models":                  {func(m obj) { row(m, 0, 0)["models"] = []any{} }, "models is empty"},
		"wildcard model":             {func(m obj) { row(m, 0, 0)["models"] = []any{"claude-*"} }, "not a model id"},
		"empty model":                {func(m obj) { row(m, 0, 0)["models"] = []any{""} }, "not a model id"},
		"non-ascii model":            {func(m obj) { row(m, 0, 0)["models"] = []any{"claude-sonnet-4-6 "} }, "not a model id"},
		"model twice in row":         {func(m obj) { row(m, 0, 1)["models"] = []any{"claude-opus-5-5", "claude-opus-5-5"} }, "duplicate row"},
		"model in two rows":          {func(m obj) { row(m, 0, 2)["models"] = []any{"claude-opus-5-5"} }, "duplicate row"},
		"no betas":                   {func(m obj) { row(m, 0, 0)["default_betas"] = []any{} }, "default_betas is empty"},
		"bad beta":                   {func(m obj) { row(m, 0, 0)["default_betas"] = []any{"Claude Code"} }, "not a beta token"},
		"beta twice":                 {func(m obj) { row(m, 0, 0)["default_betas"] = []any{"oauth-2025-04-20", "oauth-2025-04-20"} }, "listed twice"},
		"optional repeats":           {func(m obj) { row(m, 0, 0)["optional_betas"] = []any{"oauth-2025-04-20"} }, "listed twice"},
		"unknown condition":          {func(m obj) { row(m, 0, 1)["requires"] = []any{"safeguards_imply_afk"} }, "not a known condition"},
		"condition twice":            {func(m obj) { row(m, 0, 1)["requires"] = []any{"context_management", "context_management"} }, "listed twice"},
		"condition without its beta": {func(m obj) { row(m, 0, 0)["requires"] = []any{"thinking_display_updates"} }, "does not list"},
		"active version absent":      {func(m obj) { compat(m)["versions"] = []any{version(m, 1)} }, "no rows for cli_version"},
		"alias clashes":              {func(m obj) { compat(m)["aliases"] = []any{obj{"alias": "claude-opus-4-7", "model": "claude-opus-4-6"}} }, "also a row model"},
		"alias dangles":              {func(m obj) { compat(m)["aliases"] = []any{obj{"alias": "claude-x-1", "model": "claude-x"}} }, "no row lists"},
		"alias twice": {func(m obj) {
			a := obj{"alias": "claude-opus-4-7-20990101", "model": "claude-opus-4-7"}
			compat(m)["aliases"] = []any{a, a}
		}, "duplicate alias"},
	} {
		m := schema2ProfileMap(t)
		tc.edit(m)
		_, err := ParseClientProfile(marshal(t, m))
		require.ErrorContains(t, err, tc.want, name)
	}

	big := append([]byte(`{"schema_version":2,"pad":"`), make([]byte, MaxClientProfileBytes)...)
	_, err := ParseClientProfile(big)
	require.ErrorContains(t, err, "larger than")
}

// An explicit alias resolves to its model's row; nothing else does.
func TestCompatibilityAliasResolvesExplicitlyOnly(t *testing.T) {
	m := schema2ProfileMap(t)
	compat, _ := m["compatibility"].(map[string]any)
	compat["aliases"] = []any{map[string]any{"alias": "claude-opus-4-7-20990101", "model": "claude-opus-4-7"}}
	p, err := ParseClientProfile(marshal(t, m))
	require.NoError(t, err)
	viaAlias, ok := p.CompatibilityRow("2.1.318", "messages", "cli", "claude-opus-4-7-20990101")
	require.True(t, ok)
	require.Equal(t, "claude-opus-4-7", viaAlias.Model)
	_, ok = p.CompatibilityRow("2.1.318", "messages", "cli", "claude-opus-4-7-20990102")
	require.False(t, ok)
}
