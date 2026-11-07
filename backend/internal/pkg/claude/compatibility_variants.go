package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Schema 3 compatibility table (#153). A row is one exact (version, endpoint, entrypoint,
// model) and lists the request variants the real client was captured sending: each variant
// is a closed structural selector plus the exact beta lists, in wire order, seen with it.
// Nothing here is enforced yet; EvaluateNativeCompatibility and ValidateTargetCompatibility
// are pure functions the gateway does not call until enforcement is integrated.

// CompatibilitySelector is the structural case of a request body. Every field is part of
// the key: there are no wildcards.
type CompatibilitySelector struct {
	Cache             string // ttl1h | default | none
	Thinking          string // e.g. adaptive/updates, enabled/omitted, adaptive, disabled, absent
	ContextManagement bool
	Effort            bool // top-level output_config.effort
	OutputFormat      bool // top-level output_config.format
	DeferredTools     bool // at least one tool with defer_loading=true
	Safeguards        bool
	Tools             bool // a non-empty tools array
}

var (
	selectorCacheForms    = []string{"ttl1h", "default", "none"}
	selectorThinkingForms = []string{
		"adaptive/updates", "adaptive/omitted", "adaptive",
		"enabled/updates", "enabled/omitted", "enabled",
		"disabled", "absent",
	}
)

type variantDoc struct {
	Versions []variantVersionDoc `json:"versions"`
	// Aliases must be absent or empty: schema 3 lists dated model ids as plain models.
	Aliases []json.RawMessage `json:"aliases"`
}

type variantVersionDoc struct {
	Version string          `json:"version"`
	Rows    []variantRowDoc `json:"rows"`
}

type variantRowDoc struct {
	Endpoint   string            `json:"endpoint"`
	Entrypoint string            `json:"entrypoint"`
	Models     []string          `json:"models"`
	Variants   []variantEntryDoc `json:"variants"`
}

type variantEntryDoc struct {
	Selector variantSelectorDoc `json:"selector"`
	BetaSets [][]string         `json:"beta_sets"`
}

// Pointers so an omitted field is an error, never a wildcard or a silent false.
type variantSelectorDoc struct {
	Cache             *string `json:"cache"`
	Thinking          *string `json:"thinking"`
	ContextManagement *bool   `json:"context_management"`
	Effort            *bool   `json:"effort"`
	OutputFormat      *bool   `json:"output_format"`
	DeferredTools     *bool   `json:"deferred_tools"`
	Safeguards        *bool   `json:"safeguards"`
	Tools             *bool   `json:"tools"`
}

type variantRow struct {
	cells map[CompatibilitySelector][][]string // exact beta lists in wire order
}

// variantTable is built once by the parser and never mutated.
type variantTable struct {
	rows     map[compatibilityKey]*variantRow
	versions map[string]map[string]struct{} // version -> every token any of its rows sends
}

func buildVariantTable(doc *variantDoc) (*variantTable, error) {
	if len(doc.Aliases) > 0 {
		return nil, errors.New("compatibility.aliases is not supported in schema_version 3: list each dated model id in the models of the rows it was captured in, then remove aliases")
	}
	if len(doc.Versions) == 0 {
		return nil, errors.New("compatibility.versions is empty")
	}
	t := &variantTable{rows: map[compatibilityKey]*variantRow{}, versions: map[string]map[string]struct{}{}}
	for vi, v := range doc.Versions {
		where := fmt.Sprintf("compatibility.versions[%d]", vi)
		if !isStrictReleaseVersion(v.Version) {
			return nil, fmt.Errorf("%s.version must be a strict three-part release version", where)
		}
		if _, dup := t.versions[v.Version]; dup {
			return nil, fmt.Errorf("%s: duplicate version %s", where, v.Version)
		}
		t.versions[v.Version] = map[string]struct{}{}
		if len(v.Rows) == 0 {
			return nil, fmt.Errorf("%s.rows is empty", where)
		}
		for ri, r := range v.Rows {
			if err := addVariantRow(t, v.Version, r); err != nil {
				return nil, fmt.Errorf("%s.rows[%d]: %w", where, ri, err)
			}
		}
	}
	return t, nil
}

func addVariantRow(t *variantTable, version string, r variantRowDoc) error {
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
	if len(r.Variants) == 0 {
		return errors.New("variants is empty")
	}
	row := &variantRow{cells: map[CompatibilitySelector][][]string{}}
	for i, v := range r.Variants {
		sel, err := v.Selector.selector()
		if err != nil {
			return fmt.Errorf("variants[%d].selector: %w", i, err)
		}
		if _, dup := row.cells[sel]; dup {
			return fmt.Errorf("variants[%d]: duplicate selector", i)
		}
		if len(v.BetaSets) == 0 {
			return fmt.Errorf("variants[%d].beta_sets is empty", i)
		}
		var sets [][]string
		for j, list := range v.BetaSets {
			if len(list) == 0 {
				return fmt.Errorf("variants[%d].beta_sets[%d] is empty", i, j)
			}
			seen := map[string]struct{}{}
			for _, b := range list {
				if !betaTokenRe.MatchString(b) {
					return fmt.Errorf("variants[%d]: beta %q is not a beta token", i, b)
				}
				if _, dup := seen[b]; dup {
					return fmt.Errorf("variants[%d]: beta %s is listed twice", i, b)
				}
				seen[b] = struct{}{}
				t.versions[version][b] = struct{}{}
			}
			for _, prev := range sets {
				if sameTokenSet(prev, list) {
					return fmt.Errorf("variants[%d].beta_sets[%d] repeats another set", i, j)
				}
			}
			sets = append(sets, slices.Clone(list))
		}
		row.cells[sel] = sets
	}
	for _, m := range r.Models {
		if !modelIDRe.MatchString(m) {
			return fmt.Errorf("model %q is not a model id", m)
		}
		k := compatibilityKey{version, r.Endpoint, r.Entrypoint, m}
		if _, dup := t.rows[k]; dup {
			return fmt.Errorf("duplicate row for %s %s %s %s", k.version, k.endpoint, k.entrypoint, k.model)
		}
		t.rows[k] = row // shared by the row's models; never mutated after parsing
	}
	return nil
}

func (d variantSelectorDoc) selector() (CompatibilitySelector, error) {
	if d.Cache == nil || d.Thinking == nil || d.ContextManagement == nil || d.Effort == nil ||
		d.OutputFormat == nil || d.DeferredTools == nil || d.Safeguards == nil || d.Tools == nil {
		return CompatibilitySelector{}, errors.New("every selector field must be set")
	}
	if !slices.Contains(selectorCacheForms, *d.Cache) {
		return CompatibilitySelector{}, fmt.Errorf("cache %q is not a known form", *d.Cache)
	}
	if !slices.Contains(selectorThinkingForms, *d.Thinking) {
		return CompatibilitySelector{}, fmt.Errorf("thinking %q is not a known form", *d.Thinking)
	}
	return CompatibilitySelector{
		Cache: *d.Cache, Thinking: *d.Thinking, ContextManagement: *d.ContextManagement, Effort: *d.Effort,
		OutputFormat: *d.OutputFormat, DeferredTools: *d.DeferredTools, Safeguards: *d.Safeguards, Tools: *d.Tools,
	}, nil
}

func sameTokenSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// CompatibilityBetaSets returns copies of the exact beta lists a schema 3 row records for a
// selector, in wire order. Lookups are exact: no nearest version, model family or alias.
func (p ClientProfile) CompatibilityBetaSets(version, endpoint, entrypoint, model string, sel CompatibilitySelector) ([][]string, bool) {
	if p.variants == nil {
		return nil, false
	}
	row, ok := p.variants.rows[compatibilityKey{version, endpoint, entrypoint, model}]
	if !ok {
		return nil, false
	}
	sets, ok := row.cells[sel]
	if !ok {
		return nil, false
	}
	out := make([][]string, len(sets))
	for i, s := range sets {
		out[i] = slices.Clone(s)
	}
	return out, true
}
