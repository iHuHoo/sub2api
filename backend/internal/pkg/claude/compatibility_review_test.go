package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The shipped table and evidence are exactly what the checked-in generator produces from
// the checked-in sanitized evidence, with the profile's identity fields kept.
func TestShippedCompatibilityIsReproducible(t *testing.T) {
	shipped, err := os.ReadFile(profileFile)
	require.NoError(t, err)
	identity, err := ReadClientProfileIdentity(shipped)
	require.NoError(t, err)
	cells := loadEvidenceCells(t)
	got, err := RenderClientProfile(identity, cells)
	require.NoError(t, err)
	require.Equal(t, string(shipped), string(got), "run: go run ./cmd/claude-compat-gen profile -evidence internal/pkg/claude/testdata/compatibility3-evidence.json -profile resources/claude-client-profile/profile.json")

	evidence, err := os.ReadFile("testdata/compatibility3-evidence.json")
	require.NoError(t, err)
	again, err := MarshalCompatibilityEvidence(cells)
	require.NoError(t, err)
	require.Equal(t, string(evidence), string(again), "evidence file is in canonical order and format")
}

func captureLine(t *testing.T, fx compatibilityFixture, mutate func(h [][2]string) [][2]string) []byte {
	t.Helper()
	h := fx.Headers
	if mutate != nil {
		h = mutate(slices.Clone(h))
	}
	var raw []string
	for _, kv := range h {
		raw = append(raw, kv[0], kv[1])
	}
	raw = append(raw, "Authorization", "Bearer sk-ant-oat01-synthetic-not-kept")
	return marshal(t, map[string]any{"url": fx.URL, "rawHeaders": raw, "body": string(fx.Body)})
}

// The evidence step keeps only metadata, in wire order, and rejects order drift.
func TestCompatibilityEvidenceFromCaptures(t *testing.T) {
	fixtures := loadCompatibilityFixtures(t)
	var ev CompatibilityEvidence
	for _, fx := range fixtures {
		require.NoError(t, ev.AddCaptureLine(captureLine(t, fx, nil)))
	}
	require.NoError(t, ev.AddCaptureLine([]byte(`{"url":"/api/hello","rawHeaders":[],"body":""}`)), "other endpoints are skipped")
	cells := ev.Cells()
	require.Len(t, cells, len(fixtures))
	out, err := MarshalCompatibilityEvidence(cells)
	require.NoError(t, err)
	require.NotContains(t, string(out), "sk-ant")
	for _, fx := range fixtures {
		beta, _ := fixtureHeader(fx, "anthropic-beta")
		i := slices.IndexFunc(cells, func(c CompatibilityEvidenceCell) bool {
			return c.Version == fx.Version && c.Endpoint == fx.Endpoint && c.Entrypoint == fx.Entrypoint && c.Model == fx.Model
		})
		require.GreaterOrEqual(t, i, 0)
		require.Equal(t, [][]string{strings.Split(beta, ",")}, cells[i].BetaSets)
	}

	reorder := func(h [][2]string) [][2]string {
		for i := range h {
			if strings.EqualFold(h[i][0], "anthropic-beta") {
				tok := strings.Split(h[i][1], ",")
				tok[0], tok[1] = tok[1], tok[0]
				h[i][1] = strings.Join(tok, ",")
			}
		}
		return h
	}
	require.ErrorContains(t, ev.AddCaptureLine(captureLine(t, fixtures[0], reorder)), "two different orders")
	foreign := func(h [][2]string) [][2]string {
		for i := range h {
			if strings.EqualFold(h[i][0], "user-agent") {
				h[i][1] = "curl/8"
			}
		}
		return h
	}
	require.ErrorContains(t, ev.AddCaptureLine(captureLine(t, fixtures[0], foreign)), "not a Claude Code CLI identity")
}

// R1: model-dependent values are checked against the mapped target model as well.
func TestEvaluateNativeCompatibilityMappedModel(t *testing.T) {
	p := ActiveClientProfile()
	src, _ := fixtureByName(t, "2.1.318", "messages", "cli", "claude-opus-4-7")
	src.Body = editBody(t, src.Body, func(m map[string]any) { m["output_config"] = map[string]any{"effort": "xhigh"} })
	_, err := p.EvaluateNativeCompatibility(src, "claude-opus-4-7", true)
	require.NoError(t, err, "xhigh is an opus-4-7 level")
	_, err = p.EvaluateNativeCompatibility(src, "claude-opus-4-6", true)
	require.Equal(t, CompatibilityIncompatible, compatClass(t, err), "opus-4-6 has no xhigh: %v", err)

	// Positive control: a mapping between two models with the same reviewed row succeeds,
	// and the built target request (wire model replaced) passes target validation.
	var from, to string
	for k, r := range p.variants.rows {
		if k.version != "2.1.318" || k.endpoint != "messages" || k.entrypoint != "cli" || k.model != "claude-opus-4-6" {
			continue
		}
		for k2, r2 := range p.variants.rows {
			if r2 == r && k2.model != k.model && k2.version == k.version && k2.endpoint == k.endpoint && k2.entrypoint == k.entrypoint {
				from, to = k.model, k2.model
			}
		}
	}
	require.NotEmpty(t, to, "opus-4-6 shares its 2.1.318 cli row with another model")
	src, _ = fixtureByName(t, "2.1.318", "messages", "cli", from)
	got, err := p.EvaluateNativeCompatibility(src, to, true)
	require.NoError(t, err)
	built := editBody(t, src.Body, func(m map[string]any) { m["model"] = to })
	require.NoError(t, p.ValidateTargetCompatibility("messages", "cli", []string{strings.Join(got.Betas, ",")}, built))
}

// R2: per-message effort is a selector field; only reviewed cells accept it.
func TestPerMessageEffortIsASelectorField(t *testing.T) {
	p := ActiveClientProfile()
	raw, err := os.ReadFile("testdata/compatibility3-cases.json")
	require.NoError(t, err)
	var cases []evaluatorCase
	require.NoError(t, json.Unmarshal(raw, &cases))
	sawCaptured := false
	for _, tc := range cases {
		_, sel, err := bodySelector(tc.Endpoint, tc.Body, "")
		require.NoError(t, err)
		if sel.MessageEffort && tc.WantClass == "" {
			sawCaptured = true
		}
	}
	require.True(t, sawCaptured, "a captured supported case carries per-message effort")

	idx := slices.IndexFunc(cases, func(c evaluatorCase) bool { return c.Name == "2.1.318 cli compact claude-opus-4-6" })
	tc := cases[idx]
	_, sel, err := bodySelector(tc.Endpoint, tc.Body, "")
	require.NoError(t, err)
	require.False(t, sel.MessageEffort)
	appendSystem := func(effort string) []byte {
		return editBody(t, tc.Body, func(m map[string]any) {
			m["messages"] = append(msgs(m), map[string]any{"role": "system", "content": "x", "output_config": map[string]any{"effort": effort}})
		})
	}
	for effort, class := range map[string]string{"low": CompatibilityUnsupported, "ultra": CompatibilityUnsupported} {
		src := CompatibilitySource{Version: tc.Version, Endpoint: tc.Endpoint, Entrypoint: tc.Entrypoint, BetaHeaders: []string{tc.Beta}, Body: appendSystem(effort)}
		_, err := p.EvaluateNativeCompatibility(src, "claude-opus-4-6", true)
		require.Equal(t, class, compatClass(t, err), "%s: %v", effort, err)
	}
	_, _, err = bodySelector("messages", editBody(t, tc.Body, func(m map[string]any) {
		m["messages"] = append(msgs(m), map[string]any{"role": "system", "content": "x", "output_config": map[string]any{"format": map[string]any{"type": "json_schema", "schema": map[string]any{}}}})
	}), "")
	require.Equal(t, CompatibilityUnsupported, compatClass(t, err), "a system message carries effort only")
}

// R3: safeguards entries are a reviewed type with an opaque classifier_context.
func TestSafeguardsShape(t *testing.T) {
	p := ActiveClientProfile()
	src, _ := fixtureByName(t, "2.1.318", "messages", "cli", "claude-opus-4-7")
	_, sel, err := bodySelector("messages", src.Body, "")
	require.NoError(t, err)
	require.True(t, sel.Safeguards)
	opaque := editBody(t, src.Body, func(m map[string]any) {
		m["safeguards"] = []any{map[string]any{"type": "dangerous_tool_use", "classifier_context": map[string]any{"anything": []any{1, "x"}}}}
	})
	s := src
	s.Body = opaque
	_, err = p.EvaluateNativeCompatibility(s, "claude-opus-4-7", true)
	require.NoError(t, err, "classifier_context content is payload")
	for name, tc := range map[string]struct {
		sg    any
		class string
	}{
		"invented type":   {[]any{map[string]any{"type": "invented", "new_capability": true}}, CompatibilityUnsupported},
		"unknown type":    {[]any{map[string]any{"type": "invented", "classifier_context": map[string]any{}}}, CompatibilityUnsupported},
		"extra key":       {[]any{map[string]any{"type": "dangerous_tool_use", "classifier_context": map[string]any{}, "mode": "x"}}, CompatibilityUnsupported},
		"no context":      {[]any{map[string]any{"type": "dangerous_tool_use"}}, CompatibilityInvalidBody},
		"context not obj": {[]any{map[string]any{"type": "dangerous_tool_use", "classifier_context": "x"}}, CompatibilityInvalidBody},
		"empty":           {[]any{}, CompatibilityInvalidBody},
	} {
		s := src
		s.Body = editBody(t, src.Body, func(m map[string]any) { m["safeguards"] = tc.sg })
		_, err := p.EvaluateNativeCompatibility(s, "claude-opus-4-7", true)
		require.Equal(t, tc.class, compatClass(t, err), "%s: %v", name, err)
	}
}

// Reasons are bounded: they never echo request-supplied keys, models, tokens or values.
func TestCompatibilityErrorsDoNotEchoInput(t *testing.T) {
	p := ActiveClientProfile()
	src, beta := fixtureByName(t, "2.1.318", "messages", "cli", "claude-opus-4-7")
	const marker = "zzleakzz"
	tries := []CompatibilitySource{}
	add := func(edit func(s *CompatibilitySource)) {
		s := src
		edit(&s)
		tries = append(tries, s)
	}
	add(func(s *CompatibilitySource) { s.BetaHeaders = []string{beta + "," + marker + "-2026-01-01"} })
	add(func(s *CompatibilitySource) { s.BetaHeaders = []string{beta + "," + strings.Repeat(marker, 40)} })
	add(func(s *CompatibilitySource) { s.Version = "9.9." + marker })
	add(func(s *CompatibilitySource) {
		s.Body = editBody(t, src.Body, func(m map[string]any) { m[marker] = marker })
	})
	add(func(s *CompatibilitySource) {
		s.Body = editBody(t, src.Body, func(m map[string]any) { m["model"] = "claude-" + marker })
	})
	add(func(s *CompatibilitySource) {
		s.Body = editBody(t, src.Body, func(m map[string]any) { m["thinking"] = map[string]any{"type": marker} })
	})
	add(func(s *CompatibilitySource) {
		s.Body = editBody(t, src.Body, func(m map[string]any) {
			m["messages"] = append(msgs(m), map[string]any{"role": marker, "content": "x"})
		})
	})
	for i, s := range tries {
		_, err := p.EvaluateNativeCompatibility(s, "claude-opus-4-7", true)
		require.Error(t, err, i)
		require.NotContains(t, err.Error(), marker, i)
	}
	_, err := p.EvaluateNativeCompatibility(src, "claude-"+marker, true)
	require.NotContains(t, err.Error(), marker)
	require.False(t, bytes.Contains([]byte(p.ValidateTargetCompatibility("messages", "cli", []string{beta + "," + marker}, src.Body).Error()), []byte(marker)))
}
