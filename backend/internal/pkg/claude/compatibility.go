package claude

import (
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/tidwall/gjson"
)

// The compatibility table (#153, profile schema_version 2) records which betas each
// captured Claude Code version sends, per endpoint, entrypoint and exact model. It is
// loaded and validated with the profile but nothing enforces it yet: requests are
// forwarded exactly as before until native enforcement ships.

// CompatibilityRow is one captured (version, endpoint, entrypoint, model) request shape.
// DefaultBetas is the observed emission in wire order, not a claim that every token is
// mandatory in every request.
type CompatibilityRow struct {
	Version       string
	BodyContract  string
	Endpoint      string
	Entrypoint    string
	Model         string
	DefaultBetas  []string
	OptionalBetas []string
	Requires      []string
}

// Endpoints and entrypoints a row may name.
const (
	CompatibilityEndpointMessages    = "messages"
	CompatibilityEndpointCountTokens = "count_tokens"
	CompatibilityEntrypointCLI       = "cli" // interactive: UA "(external, cli)", cc_entrypoint=cli
	CompatibilityEntrypointSDK       = "sdk" // -p print mode: UA "(external, sdk-cli)", cc_entrypoint=sdk
)

// compatibilityBodyContracts are the reviewed body-shape families. Listing a name in the
// file does not make it trusted; only names registered here load.
var compatibilityBodyContracts = map[string]struct{}{
	// 2.1.309 and 2.1.318: same body keys, thinking, output_config, context_management,
	// tool names and input schemas per row (offline captures, #153).
	"cc-2.1.309": {},
}

// compatibilityCondition reads one way only: when the body matches, the request must
// carry Beta. It never implies the reverse.
type compatibilityCondition struct {
	Beta  string
	Match func(body []byte) bool
}

// compatibilityConditions are the only names a row's "requires" may use.
var compatibilityConditions = map[string]compatibilityCondition{
	"thinking_display_updates": {Beta: "thinking-display-updates-2026-08-18", Match: func(b []byte) bool {
		return gjson.GetBytes(b, "thinking.display").String() == "updates"
	}},
	"context_management": {Beta: BetaContextManagement, Match: func(b []byte) bool {
		return gjson.GetBytes(b, "context_management").Exists()
	}},
}

type compatibilityDoc struct {
	Versions []compatibilityVersionDoc `json:"versions"`
	Aliases  []compatibilityAliasDoc   `json:"aliases"`
}

type compatibilityVersionDoc struct {
	Version      string                `json:"version"`
	BodyContract string                `json:"body_contract"`
	Rows         []compatibilityRowDoc `json:"rows"`
}

type compatibilityRowDoc struct {
	Endpoint      string   `json:"endpoint"`
	Entrypoint    string   `json:"entrypoint"`
	Models        []string `json:"models"`
	DefaultBetas  []string `json:"default_betas"`
	OptionalBetas []string `json:"optional_betas"`
	Requires      []string `json:"requires"`
}

type compatibilityAliasDoc struct {
	Alias string `json:"alias"`
	Model string `json:"model"`
}

type compatibilityKey struct{ version, endpoint, entrypoint, model string }

// compatibilityTable is built once by the parser and never mutated; lookups return copies.
type compatibilityTable struct {
	rows     map[compatibilityKey]CompatibilityRow
	versions map[string]struct{}
	aliases  map[string]string
}

var (
	betaTokenRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	modelIDRe   = regexp.MustCompile(`^[a-z0-9]+([.-][a-z0-9]+)*$`)
)

func buildCompatibilityTable(doc *compatibilityDoc) (*compatibilityTable, error) {
	if len(doc.Versions) == 0 {
		return nil, errors.New("compatibility.versions is empty")
	}
	t := &compatibilityTable{
		rows:     map[compatibilityKey]CompatibilityRow{},
		versions: map[string]struct{}{},
		aliases:  map[string]string{},
	}
	for vi, v := range doc.Versions {
		where := fmt.Sprintf("compatibility.versions[%d]", vi)
		if !isStrictReleaseVersion(v.Version) {
			return nil, fmt.Errorf("%s.version must be a strict three-part release version", where)
		}
		if _, dup := t.versions[v.Version]; dup {
			return nil, fmt.Errorf("%s: duplicate version %s", where, v.Version)
		}
		t.versions[v.Version] = struct{}{}
		if _, ok := compatibilityBodyContracts[v.BodyContract]; !ok {
			return nil, fmt.Errorf("%s.body_contract %q is not a known contract", where, v.BodyContract)
		}
		if len(v.Rows) == 0 {
			return nil, fmt.Errorf("%s.rows is empty", where)
		}
		for ri, r := range v.Rows {
			if err := addCompatibilityRow(t, v, r); err != nil {
				return nil, fmt.Errorf("%s.rows[%d]: %w", where, ri, err)
			}
		}
	}
	canonical := map[string]struct{}{}
	for k := range t.rows {
		canonical[k.model] = struct{}{}
	}
	for ai, a := range doc.Aliases {
		where := fmt.Sprintf("compatibility.aliases[%d]", ai)
		if !modelIDRe.MatchString(a.Alias) || !modelIDRe.MatchString(a.Model) {
			return nil, fmt.Errorf("%s: alias and model must be model ids", where)
		}
		if _, dup := t.aliases[a.Alias]; dup {
			return nil, fmt.Errorf("%s: duplicate alias %s", where, a.Alias)
		}
		if _, clash := canonical[a.Alias]; clash {
			return nil, fmt.Errorf("%s: alias %s is also a row model", where, a.Alias)
		}
		if _, ok := canonical[a.Model]; !ok {
			return nil, fmt.Errorf("%s: alias %s points at %s, which no row lists", where, a.Alias, a.Model)
		}
		t.aliases[a.Alias] = a.Model
	}
	return t, nil
}

func addCompatibilityRow(t *compatibilityTable, v compatibilityVersionDoc, r compatibilityRowDoc) error {
	switch r.Endpoint {
	case CompatibilityEndpointMessages, CompatibilityEndpointCountTokens:
	default:
		return fmt.Errorf("endpoint %q is not messages or count_tokens", r.Endpoint)
	}
	switch r.Entrypoint {
	case CompatibilityEntrypointCLI, CompatibilityEntrypointSDK:
	default:
		return fmt.Errorf("entrypoint %q is not cli or sdk", r.Entrypoint)
	}
	if len(r.Models) == 0 {
		return errors.New("models is empty")
	}
	if len(r.DefaultBetas) == 0 {
		return errors.New("default_betas is empty")
	}
	betas := map[string]struct{}{}
	for _, list := range [][]string{r.DefaultBetas, r.OptionalBetas} {
		for _, b := range list {
			if !betaTokenRe.MatchString(b) {
				return fmt.Errorf("beta %q is not a beta token", b)
			}
			if _, dup := betas[b]; dup {
				return fmt.Errorf("beta %s is listed twice", b)
			}
			betas[b] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	for _, name := range r.Requires {
		cond, ok := compatibilityConditions[name]
		if !ok {
			return fmt.Errorf("requires %q is not a known condition", name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("requires %s is listed twice", name)
		}
		seen[name] = struct{}{}
		if _, ok := betas[cond.Beta]; !ok {
			return fmt.Errorf("requires %s needs beta %s, which the row does not list", name, cond.Beta)
		}
	}
	for _, m := range r.Models {
		if !modelIDRe.MatchString(m) {
			return fmt.Errorf("model %q is not a model id", m)
		}
		k := compatibilityKey{v.Version, r.Endpoint, r.Entrypoint, m}
		if _, dup := t.rows[k]; dup {
			return fmt.Errorf("duplicate row for %s %s %s %s", k.version, k.endpoint, k.entrypoint, k.model)
		}
		t.rows[k] = CompatibilityRow{
			Version: v.Version, BodyContract: v.BodyContract, Endpoint: r.Endpoint, Entrypoint: r.Entrypoint, Model: m,
			DefaultBetas: slices.Clone(r.DefaultBetas), OptionalBetas: slices.Clone(r.OptionalBetas), Requires: slices.Clone(r.Requires),
		}
	}
	return nil
}

// HasCompatibility reports whether the profile carries a compatibility table (schema 2).
func (p ClientProfile) HasCompatibility() bool { return p.compat != nil }

// CompatibilityRowCount is the number of expanded (version, endpoint, entrypoint, model) rows.
func (p ClientProfile) CompatibilityRowCount() int {
	if p.compat == nil {
		return 0
	}
	return len(p.compat.rows)
}

// CompatibilityRow returns a copy of the row for an exact version, endpoint, entrypoint and
// model (explicit aliases resolve to their model). There is no nearest-version or
// model-family fallback.
func (p ClientProfile) CompatibilityRow(version, endpoint, entrypoint, model string) (CompatibilityRow, bool) {
	if p.compat == nil {
		return CompatibilityRow{}, false
	}
	if m, ok := p.compat.aliases[model]; ok {
		model = m
	}
	r, ok := p.compat.rows[compatibilityKey{version, endpoint, entrypoint, model}]
	if !ok {
		return CompatibilityRow{}, false
	}
	r.DefaultBetas = slices.Clone(r.DefaultBetas)
	r.OptionalBetas = slices.Clone(r.OptionalBetas)
	r.Requires = slices.Clone(r.Requires)
	return r, true
}
