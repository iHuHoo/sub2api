package claude

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type evidenceCell struct {
	Version    string          `json:"version"`
	Endpoint   string          `json:"endpoint"`
	Entrypoint string          `json:"entrypoint"`
	Model      string          `json:"model"`
	Selector   evidenceSelJSON `json:"selector"`
	BetaSets   [][]string      `json:"beta_sets"`
}

type evidenceSelJSON struct {
	Cache             string `json:"cache"`
	Thinking          string `json:"thinking"`
	ContextManagement bool   `json:"context_management"`
	Effort            bool   `json:"effort"`
	OutputFormat      bool   `json:"output_format"`
	DeferredTools     bool   `json:"deferred_tools"`
	Safeguards        bool   `json:"safeguards"`
	Tools             bool   `json:"tools"`
}

func (s evidenceSelJSON) selector() CompatibilitySelector { return CompatibilitySelector(s) }

var datedModelRe = regexp.MustCompile(`-\d{8}$`)

func loadEvidenceCells(t *testing.T) []evidenceCell {
	t.Helper()
	raw, err := os.ReadFile("testdata/compatibility3-evidence.json")
	require.NoError(t, err)
	var doc struct {
		Cells []evidenceCell `json:"cells"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Cells)
	return doc.Cells
}

// Every shipped beta list is a captured one, and every captured list is shipped, except
// dated model ids, which the shipped profile leaves out (not offered to customers by default).
func TestShippedVariantsMatchEvidence(t *testing.T) {
	p := ActiveClientProfile()
	require.Equal(t, 3, p.SchemaVersion)
	shipped := 0
	for k, r := range p.variants.rows {
		require.False(t, datedModelRe.MatchString(k.model), "dated model %s is shipped", k.model)
		for _, sets := range r.cells {
			shipped += len(sets)
		}
	}
	want := 0
	for _, c := range loadEvidenceCells(t) {
		sets, ok := p.CompatibilityBetaSets(c.Version, c.Endpoint, c.Entrypoint, c.Model, c.Selector.selector())
		if datedModelRe.MatchString(c.Model) {
			require.False(t, ok, c.Model)
			continue
		}
		want++
		require.True(t, ok, "%+v", c)
		require.ElementsMatch(t, c.BetaSets, sets, "%+v", c)
		want += len(c.BetaSets) - 1
	}
	require.Equal(t, want, shipped, "nothing shipped beyond the captures")
}

type evaluatorCase struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	Endpoint   string          `json:"endpoint"`
	Entrypoint string          `json:"entrypoint"`
	Beta       string          `json:"beta"`
	NoCredit   bool            `json:"no_credit"`
	Body       json.RawMessage `json:"body"`
	WantBetas  []string        `json:"want_betas"`
	WantClass  string          `json:"want_class"`
}

func compatClass(t *testing.T, err error) string {
	t.Helper()
	var ce *CompatibilityError
	require.True(t, errors.As(err, &ce), "%v", err)
	return ce.Class
}

// Sanitized captures of compact, thinking-off, title, 1M and 2.1.280 requests; the expected
// betas are the captured 2.1.318 request of the same shape, picked outside this evaluator.
func TestEvaluateNativeCompatibilityCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/compatibility3-cases.json")
	require.NoError(t, err)
	var cases []evaluatorCase
	require.NoError(t, json.Unmarshal(raw, &cases))
	require.NotEmpty(t, cases)
	p := ActiveClientProfile()
	for _, tc := range cases {
		model, _, err := bodySelector(tc.Endpoint, tc.Body)
		require.NoError(t, err, tc.Name)
		src := CompatibilitySource{Version: tc.Version, Endpoint: tc.Endpoint, Entrypoint: tc.Entrypoint, BetaHeaders: []string{tc.Beta}, Body: tc.Body}
		got, err := p.EvaluateNativeCompatibility(src, model, tc.NoCredit)
		if tc.WantClass != "" {
			require.Error(t, err, tc.Name)
			require.Equal(t, tc.WantClass, compatClass(t, err), "%s: %v", tc.Name, err)
			continue
		}
		require.NoError(t, err, tc.Name)
		require.Equal(t, tc.WantBetas, got.Betas, tc.Name)
		require.Equal(t, "2.1.318", got.Version)
		require.NoError(t, p.ValidateTargetCompatibility(tc.Endpoint, tc.Entrypoint, []string{strings.Join(got.Betas, ",")}, tc.Body), tc.Name)
	}
}

// The single-turn 309/318 fixtures: a 309 request maps to exactly what 318 sent for the same
// endpoint, entrypoint and model; a 318 request maps to itself.
func TestEvaluateNativeCompatibilitySingleTurnFixtures(t *testing.T) {
	p := ActiveClientProfile()
	byKey := map[string]compatibilityFixture{}
	fixtures := loadCompatibilityFixtures(t)
	for _, fx := range fixtures {
		if fx.Version == "2.1.318" {
			byKey[fx.Endpoint+" "+fx.Entrypoint+" "+fx.Model] = fx
		}
	}
	for _, fx := range fixtures {
		name := fx.Version + " " + fx.Endpoint + " " + fx.Entrypoint + " " + fx.Model
		beta, _ := fixtureHeader(fx, "anthropic-beta")
		got, err := p.EvaluateNativeCompatibility(CompatibilitySource{
			Version: fx.Version, Endpoint: fx.Endpoint, Entrypoint: fx.Entrypoint, BetaHeaders: []string{beta}, Body: fx.Body,
		}, fx.Model, true)
		require.NoError(t, err, name)
		target, ok := byKey[fx.Endpoint+" "+fx.Entrypoint+" "+fx.Model]
		require.True(t, ok, name)
		want, _ := fixtureHeader(target, "anthropic-beta")
		require.Equal(t, strings.Split(want, ","), got.Betas, name)
		if fx.Version == "2.1.318" {
			require.NoError(t, p.ValidateTargetCompatibility(fx.Endpoint, fx.Entrypoint, []string{beta}, fx.Body), name)
			swapped := strings.Split(beta, ",")
			swapped[0], swapped[1] = swapped[1], swapped[0]
			err := p.ValidateTargetCompatibility(fx.Endpoint, fx.Entrypoint, []string{strings.Join(swapped, ",")}, fx.Body)
			require.Equal(t, CompatibilityUnsupported, compatClass(t, err), "target order is exact: %s", name)
		}
	}
}

func fixtureByName(t *testing.T, version, endpoint, entrypoint, model string) (CompatibilitySource, string) {
	t.Helper()
	for _, fx := range loadCompatibilityFixtures(t) {
		if fx.Version == version && fx.Endpoint == endpoint && fx.Entrypoint == entrypoint && fx.Model == model {
			beta, _ := fixtureHeader(fx, "anthropic-beta")
			return CompatibilitySource{Version: version, Endpoint: endpoint, Entrypoint: entrypoint, BetaHeaders: []string{beta}, Body: fx.Body}, beta
		}
	}
	t.Fatalf("no fixture %s %s %s %s", version, endpoint, entrypoint, model)
	return CompatibilitySource{}, ""
}

func msgs(m map[string]any) []any {
	l, _ := m["messages"].([]any)
	return l
}

func editBody(t *testing.T, body []byte, edit func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	edit(m)
	return marshal(t, m)
}

// The target follows the configured cli_version, so upgrading the profile upgrades the rules.
func TestEvaluateNativeCompatibilityTargetsConfiguredVersion(t *testing.T) {
	m := shippedProfileMap(t)
	m["cli_version"], m["user_agent"] = "2.1.309", "claude-cli/2.1.309 (external, cli)"
	p, err := ParseClientProfile(marshal(t, m))
	require.NoError(t, err)
	src, _ := fixtureByName(t, "2.1.318", "messages", "cli", "claude-opus-4-7")
	_, want := fixtureByName(t, "2.1.309", "messages", "cli", "claude-opus-4-7")
	got, err := p.EvaluateNativeCompatibility(src, "claude-opus-4-7", true)
	require.NoError(t, err)
	require.Equal(t, "2.1.309", got.Version)
	require.Equal(t, strings.Split(want, ","), got.Betas)

	m["cli_version"], m["user_agent"] = "2.1.319", "claude-cli/2.1.319 (external, cli)"
	_, err = ParseClientProfile(marshal(t, m))
	require.ErrorContains(t, err, "no rows for cli_version", "a version without captured rows cannot be configured")
}

func TestEvaluateNativeCompatibilityHeadersAndIdentity(t *testing.T) {
	p := ActiveClientProfile()
	src, beta := fixtureByName(t, "2.1.318", "messages", "cli", "claude-sonnet-4-6")
	toks := strings.Split(beta, ",")
	want, err := p.EvaluateNativeCompatibility(src, "claude-sonnet-4-6", true)
	require.NoError(t, err)

	// Repeated header values, spaces, other order and repeats mean the same set.
	split := src
	split.BetaHeaders = []string{strings.Join(toks[3:], " , "), toks[2] + "," + toks[1], toks[0] + "," + toks[0]}
	got, err := p.EvaluateNativeCompatibility(split, "claude-sonnet-4-6", true)
	require.NoError(t, err)
	require.Equal(t, want.Betas, got.Betas)

	for name, tc := range map[string]struct {
		edit  func(s *CompatibilitySource)
		model string
		class string
	}{
		"empty token":         {func(s *CompatibilitySource) { s.BetaHeaders = []string{beta + ","} }, "", CompatibilityInvalidBeta},
		"upper case token":    {func(s *CompatibilitySource) { s.BetaHeaders = []string{strings.ToUpper(beta)} }, "", CompatibilityInvalidBeta},
		"no beta header":      {func(s *CompatibilitySource) { s.BetaHeaders = nil }, "", CompatibilityUnsupported},
		"token missing":       {func(s *CompatibilitySource) { s.BetaHeaders = []string{strings.Join(toks[1:], ",")} }, "", CompatibilityUnsupported},
		"unknown token":       {func(s *CompatibilitySource) { s.BetaHeaders = []string{beta + ",new-thing-2026-12-01"} }, "", CompatibilityUnsupported},
		"invented by mimic":   {func(s *CompatibilitySource) { s.BetaHeaders = []string{beta + "," + BetaThinkingBindingControls} }, "", CompatibilityUnsupported},
		"fast without filter": {func(s *CompatibilitySource) { s.BetaHeaders = []string{beta + "," + BetaFastMode} }, "", ""},
		"bad version":         {func(s *CompatibilitySource) { s.Version = "v2.1.318" }, "", CompatibilityInvalidIdentity},
		"unreviewed version":  {func(s *CompatibilitySource) { s.Version = "2.1.412" }, "", CompatibilityUnsupported},
		"bad entrypoint":      {func(s *CompatibilitySource) { s.Entrypoint = "sdk-cli" }, "", CompatibilityInvalidIdentity},
		"bad target model":    {func(s *CompatibilitySource) {}, "claude sonnet", CompatibilityInvalidModel},
		"unknown target":      {func(s *CompatibilitySource) {}, "claude-sonnet-9-9", CompatibilityUnsupported},
		"dated not shipped":   {func(s *CompatibilitySource) {}, "claude-sonnet-4-5-20250929", CompatibilityUnsupported},
	} {
		s := src
		tc.edit(&s)
		model := "claude-sonnet-4-6"
		if tc.model != "" {
			model = tc.model
		}
		_, err := p.EvaluateNativeCompatibility(s, model, true)
		if tc.class == "" {
			require.NoError(t, err, "%s: the no-credit filter removes fast-mode before matching", name)
			continue
		}
		require.Equal(t, tc.class, compatClass(t, err), "%s: %v", name, err)
	}

	// A built target request may not repeat a token.
	err = p.ValidateTargetCompatibility("messages", "cli", []string{beta, toks[0]}, src.Body)
	require.Equal(t, CompatibilityInvalidBeta, compatClass(t, err))
}

// fallback-credit survives only where the target model's captured request carries it too.
func TestEvaluateNativeCompatibilityFallbackCredit(t *testing.T) {
	raw, err := os.ReadFile("testdata/compatibility3-cases.json")
	require.NoError(t, err)
	var cases []evaluatorCase
	require.NoError(t, json.Unmarshal(raw, &cases))
	idx := slices.IndexFunc(cases, func(c evaluatorCase) bool { return c.Name == "2.1.309 cli compact claude-sonnet-5-5" })
	require.GreaterOrEqual(t, idx, 0)
	tc := cases[idx]
	require.Contains(t, tc.Beta, BetaFallbackCreditLegacy)
	src := CompatibilitySource{Version: tc.Version, Endpoint: tc.Endpoint, Entrypoint: tc.Entrypoint, BetaHeaders: []string{tc.Beta}, Body: tc.Body}
	p := ActiveClientProfile()
	got, err := p.EvaluateNativeCompatibility(src, "claude-sonnet-5-5", true)
	require.NoError(t, err)
	require.Contains(t, got.Betas, BetaFallbackCreditLegacy)
	_, err = p.EvaluateNativeCompatibility(src, "claude-sonnet-4-6", true)
	require.Equal(t, CompatibilityUnsupported, compatClass(t, err), "2.1.318 sends fallback-credit, never for 4.6: %v", err)
}

// User payload is not part of the decision; unreviewed capability shapes are.
func TestCompatibilityBodyGrammar(t *testing.T) {
	p := ActiveClientProfile()
	src, _ := fixtureByName(t, "2.1.318", "messages", "cli", "claude-opus-4-7")
	want, err := p.EvaluateNativeCompatibility(src, "claude-opus-4-7", true)
	require.NoError(t, err)

	first := func(m map[string]any, k string) map[string]any {
		l, _ := m[k].([]any)
		o, _ := l[0].(map[string]any)
		return o
	}
	payload := editBody(t, src.Body, func(m map[string]any) {
		tl := first(m, "tools")
		tl["name"], tl["description"] = "mcp__custom__anything", "operator text"
		tl["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{"cache_control": map[string]any{"ttl": "5m"}}}
		m["messages"] = append(msgs(m), map[string]any{"role": "assistant", "content": "free text"})
	})
	s := src
	s.Body = payload
	got, err := p.EvaluateNativeCompatibility(s, "claude-opus-4-7", true)
	require.NoError(t, err)
	require.Equal(t, want.Betas, got.Betas)

	for name, tc := range map[string]struct {
		edit  func(m map[string]any)
		class string
	}{
		"tool_choice":        {func(m map[string]any) { m["tool_choice"] = map[string]any{"type": "auto"} }, CompatibilityUnsupported},
		"stop_sequences":     {func(m map[string]any) { m["stop_sequences"] = []any{"x"} }, CompatibilityUnsupported},
		"top_k":              {func(m map[string]any) { m["top_k"] = 5 }, CompatibilityUnsupported},
		"speed fast":         {func(m map[string]any) { m["speed"] = "fast" }, CompatibilityUnsupported},
		"server tool":        {func(m map[string]any) { first(m, "tools")["type"] = "web_search_20260209" }, CompatibilityUnsupported},
		"tool cache_control": {func(m map[string]any) { first(m, "tools")["cache_control"] = map[string]any{"type": "ephemeral"} }, CompatibilityUnsupported},
		"document block": {func(m map[string]any) {
			m["messages"] = append(msgs(m), map[string]any{"role": "user", "content": []any{map[string]any{"type": "document"}}})
		}, CompatibilityUnsupported},
		"redacted thinking": {func(m map[string]any) {
			m["messages"] = append(msgs(m), map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "redacted_thinking", "data": "x"}}})
		}, CompatibilityUnsupported},
		"direct image": {func(m map[string]any) {
			m["messages"] = append(msgs(m), map[string]any{"role": "user", "content": []any{map[string]any{"type": "image"}}})
		}, CompatibilityUnsupported},
		"5m ttl": {func(m map[string]any) {
			first(m, "system")["cache_control"] = map[string]any{"type": "ephemeral", "ttl": "5m"}
		}, CompatibilityUnsupported},
		"unknown effort": {func(m map[string]any) { m["output_config"] = map[string]any{"effort": "ultra"} }, CompatibilityUnsupported},
		"other cm edit": {func(m map[string]any) {
			m["context_management"] = map[string]any{"edits": []any{map[string]any{"type": "clear_tool_uses_20250919"}}}
		}, CompatibilityUnsupported},
		"thinking not object": {func(m map[string]any) { m["thinking"] = []any{} }, CompatibilityInvalidBody},
		"empty cm":            {func(m map[string]any) { m["context_management"] = map[string]any{} }, CompatibilityInvalidBody},
		"empty output_config": {func(m map[string]any) { m["output_config"] = map[string]any{} }, CompatibilityInvalidBody},
		"tools not array":     {func(m map[string]any) { m["tools"] = map[string]any{} }, CompatibilityInvalidBody},
		"no messages":         {func(m map[string]any) { m["messages"] = []any{} }, CompatibilityInvalidBody},
		"bad model":           {func(m map[string]any) { m["model"] = 7 }, CompatibilityInvalidModel},
		// Same token set, different structure: 318 never sent this pairing.
		"thinking off": {func(m map[string]any) {
			m["thinking"] = map[string]any{"type": "disabled"}
			delete(m, "context_management")
		}, CompatibilityUnsupported},
		"no tools": {func(m map[string]any) { m["tools"] = []any{} }, CompatibilityUnsupported},
	} {
		s := src
		s.Body = editBody(t, src.Body, tc.edit)
		_, err := p.EvaluateNativeCompatibility(s, "claude-opus-4-7", true)
		require.Error(t, err, name)
		require.Equal(t, tc.class, compatClass(t, err), "%s: %v", name, err)
	}

	_, _, err = bodySelector("messages", []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"x"}]} {}`))
	require.Equal(t, CompatibilityInvalidBody, compatClass(t, err))
	_, _, err = bodySelector("count_tokens", []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"x"}],"thinking":{"type":"disabled"}}`))
	require.Equal(t, CompatibilityUnsupported, compatClass(t, err), "count_tokens bodies carry model, messages and tools only")
}

func TestParseVariantTableRejectsBadTables(t *testing.T) {
	type obj = map[string]any
	item := func(list any, i int) obj {
		l, _ := list.([]any)
		o, _ := l[i].(obj)
		return o
	}
	compat := func(m obj) obj { o, _ := m["compatibility"].(obj); return o }
	row := func(m obj, r int) obj { return item(item(compat(m)["versions"], 0)["rows"], r) }
	variant := func(m obj, r, v int) obj { return item(row(m, r)["variants"], v) }
	sel := func(m obj) obj { s, _ := variant(m, 0, 0)["selector"].(obj); return s }
	for name, tc := range map[string]struct {
		edit func(m obj)
		want string
	}{
		"aliases":          {func(m obj) { compat(m)["aliases"] = []any{obj{"alias": "a", "model": "b"}} }, "list each dated model id"},
		"unknown key":      {func(m obj) { variant(m, 0, 0)["optional"] = true }, "unknown field"},
		"missing selector": {func(m obj) { delete(sel(m), "safeguards") }, "every selector field must be set"},
		"null selector":    {func(m obj) { sel(m)["tools"] = nil }, "every selector field must be set"},
		"wildcard cache":   {func(m obj) { sel(m)["cache"] = "*" }, "not a known form"},
		"bad thinking":     {func(m obj) { sel(m)["thinking"] = "adaptive/summarized" }, "not a known form"},
		"duplicate selector": {func(m obj) {
			r := row(m, 0)
			vs, _ := r["variants"].([]any)
			r["variants"] = append(vs, variant(m, 0, 0))
		}, "duplicate selector"},
		"repeated set": {func(m obj) {
			v := variant(m, 0, 0)
			sets, _ := v["beta_sets"].([]any)
			s, _ := sets[0].([]any)
			rev := slices.Clone(s)
			slices.Reverse(rev)
			v["beta_sets"] = []any{s, rev}
		}, "repeats another set"},
		"no sets":      {func(m obj) { variant(m, 0, 0)["beta_sets"] = []any{} }, "beta_sets is empty"},
		"empty set":    {func(m obj) { variant(m, 0, 0)["beta_sets"] = []any{[]any{}} }, "is empty"},
		"bad token":    {func(m obj) { variant(m, 0, 0)["beta_sets"] = []any{[]any{"Claude Code"}} }, "not a beta token"},
		"no variants":  {func(m obj) { row(m, 0)["variants"] = []any{} }, "variants is empty"},
		"model twice":  {func(m obj) { row(m, 1)["models"] = row(m, 0)["models"] }, "duplicate row"},
		"bad endpoint": {func(m obj) { row(m, 0)["endpoint"] = "complete" }, "endpoint"},
		"legacy shape": {func(m obj) { row(m, 0)["default_betas"] = []any{"oauth-2025-04-20"} }, "unknown field"},
	} {
		m := shippedProfileMap(t)
		tc.edit(m)
		_, err := ParseClientProfile(marshal(t, m))
		require.ErrorContains(t, err, tc.want, name)
	}
	// Empty aliases are accepted.
	m := shippedProfileMap(t)
	compat(m)["aliases"] = []any{}
	_, err := ParseClientProfile(marshal(t, m))
	require.NoError(t, err)
}

func TestCompatibilityBetaSetsAreCopies(t *testing.T) {
	p := ActiveClientProfile()
	cells := loadEvidenceCells(t)
	c := cells[slices.IndexFunc(cells, func(c evidenceCell) bool { return !datedModelRe.MatchString(c.Model) })]
	a, ok := p.CompatibilityBetaSets(c.Version, c.Endpoint, c.Entrypoint, c.Model, c.Selector.selector())
	require.True(t, ok)
	a[0][0] = "mutated"
	b, _ := p.CompatibilityBetaSets(c.Version, c.Endpoint, c.Entrypoint, c.Model, c.Selector.selector())
	require.NotEqual(t, "mutated", b[0][0])
}
