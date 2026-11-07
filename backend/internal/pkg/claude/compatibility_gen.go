package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Reproducible generation of the schema 3 table (#153). Two steps, both deterministic:
//
//  1. CompatibilityEvidence.AddCaptureLine reads offline receiver captures, one JSON object
//     per line: {"url": "/v1/messages?beta=true", "rawHeaders": ["Name", "value", ...],
//     "body": "<request body as a string>"}. Only metadata survives: version and entrypoint
//     from User-Agent, endpoint from the URL, model from the body, the structural selector
//     and the beta list in wire order. Prompts, payload, signatures and credentials are dropped.
//  2. RenderClientProfile turns the sanitized evidence cells into the profile's compatibility
//     section, keeping the profile's identity fields.
//
// cmd/claude-compat-gen wraps both; the evidence file in testdata is the repo-contained input.

// CompatibilityEvidenceCell is one sanitized evidence record: every exact beta list captured
// for one version, endpoint, entrypoint, model and selector, and how many requests carried it.
type CompatibilityEvidenceCell struct {
	Version    string                `json:"version"`
	Endpoint   string                `json:"endpoint"`
	Entrypoint string                `json:"entrypoint"`
	Model      string                `json:"model"`
	Selector   CompatibilitySelector `json:"selector"`
	BetaSets   [][]string            `json:"beta_sets"`
	Requests   int                   `json:"requests"`
}

// ShippedModelExcluded is the shipping policy: dated model ids (…-YYYYMMDD) stay in the
// evidence but are not offered to customers by default, so the shipped table leaves them out.
func ShippedModelExcluded(model string) bool { return datedModelIDRe.MatchString(model) }

var (
	datedModelIDRe  = regexp.MustCompile(`-\d{8}$`)
	captureUAPrefix = regexp.MustCompile(`^claude-cli/(\d+\.\d+\.\d+) \(external, (cli|sdk-cli)\)$`)
)

// CompatibilityEvidence accumulates capture lines.
type CompatibilityEvidence struct {
	cells map[evidenceKey]*CompatibilityEvidenceCell
}

type evidenceKey struct {
	compatibilityKey
	sel CompatibilitySelector
}

// AddCaptureLine adds one receiver capture line. Requests that are not messages or
// count_tokens are skipped; any capture the reviewed grammar does not accept is an error,
// because the shipped grammar must cover everything the real client sent.
func (e *CompatibilityEvidence) AddCaptureLine(line []byte) error {
	var r struct {
		URL        string   `json:"url"`
		RawHeaders []string `json:"rawHeaders"`
		Body       string   `json:"body"`
	}
	if err := json.Unmarshal(line, &r); err != nil {
		return fmt.Errorf("capture line: %w", err)
	}
	path, _, _ := strings.Cut(r.URL, "?")
	endpoint := map[string]string{"/v1/messages": CompatibilityEndpointMessages, "/v1/messages/count_tokens": CompatibilityEndpointCountTokens}[path]
	if endpoint == "" {
		return nil
	}
	if len(r.RawHeaders)%2 != 0 {
		return errors.New("capture line: rawHeaders is not name/value pairs")
	}
	var uas, betas []string
	for i := 0; i < len(r.RawHeaders); i += 2 {
		switch strings.ToLower(r.RawHeaders[i]) {
		case "user-agent":
			uas = append(uas, r.RawHeaders[i+1])
		case "anthropic-beta":
			betas = append(betas, r.RawHeaders[i+1])
		}
	}
	if len(uas) != 1 || len(betas) > 1 {
		return errors.New("capture line: want one User-Agent and at most one anthropic-beta")
	}
	m := captureUAPrefix.FindStringSubmatch(uas[0])
	if m == nil {
		return errors.New("capture line: User-Agent is not a Claude Code CLI identity")
	}
	entrypoint := map[string]string{"cli": CompatibilityEntrypointCLI, "sdk-cli": CompatibilityEntrypointSDK}[m[2]]
	tokens, err := parseBetaHeaders(betas, false)
	if err != nil {
		return fmt.Errorf("capture line: %w", err)
	}
	tokens = slices.DeleteFunc(tokens, func(b string) bool { return slices.Contains(noCreditDroppedBetas, b) })
	model, sel, err := bodySelector(endpoint, []byte(r.Body), "")
	if err != nil {
		return fmt.Errorf("capture line (%s %s %s): %w", m[1], endpoint, entrypoint, err)
	}
	if e.cells == nil {
		e.cells = map[evidenceKey]*CompatibilityEvidenceCell{}
	}
	k := evidenceKey{compatibilityKey{m[1], endpoint, entrypoint, model}, sel}
	c := e.cells[k]
	if c == nil {
		c = &CompatibilityEvidenceCell{Version: m[1], Endpoint: endpoint, Entrypoint: entrypoint, Model: model, Selector: sel}
		e.cells[k] = c
	}
	c.Requests++
	if prev := findSet(c.BetaSets, tokens); prev != nil {
		if !slices.Equal(prev, tokens) {
			return errors.New("capture line: one beta set was captured in two different orders")
		}
		return nil
	}
	c.BetaSets = append(c.BetaSets, tokens)
	return nil
}

// Cells returns the evidence in a deterministic order.
func (e *CompatibilityEvidence) Cells() []CompatibilityEvidenceCell {
	out := make([]CompatibilityEvidenceCell, 0, len(e.cells))
	for _, c := range e.cells {
		cc := *c
		cc.BetaSets = slices.Clone(c.BetaSets)
		sortBetaSets(cc.BetaSets)
		out = append(out, cc)
	}
	slices.SortFunc(out, func(a, b CompatibilityEvidenceCell) int {
		return strings.Compare(cellSortKey(a), cellSortKey(b))
	})
	return out
}

func cellSortKey(c CompatibilityEvidenceCell) string {
	sel, _ := json.Marshal(c.Selector)
	return strings.Join([]string{c.Version, c.Endpoint, c.Entrypoint, c.Model, string(sel)}, "\x00")
}

func sortBetaSets(sets [][]string) {
	slices.SortFunc(sets, func(a, b []string) int { return strings.Compare(strings.Join(a, ","), strings.Join(b, ",")) })
}

// MarshalCompatibilityEvidence renders cells one per line, the checked-in evidence format.
func MarshalCompatibilityEvidence(cells []CompatibilityEvidenceCell) ([]byte, error) {
	lines := make([]string, len(cells))
	for i, c := range cells {
		b, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		lines[i] = string(b)
	}
	return []byte("{\"cells\": [\n" + strings.Join(lines, ",\n") + "\n]}\n"), nil
}

// ParseCompatibilityEvidence reads the checked-in evidence format.
func ParseCompatibilityEvidence(raw []byte) ([]CompatibilityEvidenceCell, error) {
	var doc struct {
		Cells []CompatibilityEvidenceCell `json:"cells"`
	}
	if err := decodeStrict(raw, &doc); err != nil {
		return nil, err
	}
	if len(doc.Cells) == 0 {
		return nil, errors.New("evidence has no cells")
	}
	return doc.Cells, nil
}

// ReadClientProfileIdentity decodes only a profile's identity fields, ignoring its table, so a
// profile whose table predates the current schema can still be regenerated.
func ReadClientProfileIdentity(raw []byte) (ClientProfile, error) {
	var doc struct {
		ClientProfile
		Compatibility json.RawMessage `json:"compatibility"`
	}
	if err := decodeStrict(raw, &doc); err != nil {
		return ClientProfile{}, err
	}
	return doc.ClientProfile, nil
}

// RenderClientProfile returns identity with schema_version 3 and the compatibility section
// generated from cells (shipping policy applied), validated by ParseClientProfile. Rows
// group the models whose variants are identical; every order is deterministic.
func RenderClientProfile(identity ClientProfile, cells []CompatibilityEvidenceCell) ([]byte, error) {
	type group struct {
		version, endpoint, entrypoint string
		variants                      []variantEntryDoc
		models                        []string
	}
	perModel := map[compatibilityKey]map[string][][]string{} // selector JSON -> sets
	for _, c := range cells {
		if ShippedModelExcluded(c.Model) {
			continue
		}
		k := compatibilityKey{c.Version, c.Endpoint, c.Entrypoint, c.Model}
		if perModel[k] == nil {
			perModel[k] = map[string][][]string{}
		}
		sel, err := json.Marshal(c.Selector)
		if err != nil {
			return nil, err
		}
		perModel[k][string(sel)] = append(perModel[k][string(sel)], c.BetaSets...)
	}
	groups := map[string]*group{}
	for k, sels := range perModel {
		var variants []variantEntryDoc
		for _, s := range slices.Sorted(maps.Keys(sels)) {
			var sd variantSelectorDoc
			if err := json.Unmarshal([]byte(s), &sd); err != nil {
				return nil, err
			}
			sets := slices.Clone(sels[s])
			sortBetaSets(sets)
			variants = append(variants, variantEntryDoc{Selector: sd, BetaSets: sets})
		}
		vj, err := json.Marshal(variants)
		if err != nil {
			return nil, err
		}
		gk := strings.Join([]string{k.version, k.endpoint, k.entrypoint, string(vj)}, "\x00")
		if groups[gk] == nil {
			groups[gk] = &group{version: k.version, endpoint: k.endpoint, entrypoint: k.entrypoint, variants: variants}
		}
		groups[gk].models = append(groups[gk].models, k.model)
	}
	byVersion := map[string][]*group{}
	for _, g := range groups {
		slices.Sort(g.models)
		byVersion[g.version] = append(byVersion[g.version], g)
	}
	var doc variantDoc
	for _, v := range slices.Sorted(maps.Keys(byVersion)) {
		gs := byVersion[v]
		slices.SortFunc(gs, func(a, b *group) int {
			return strings.Compare(a.endpoint+"\x00"+a.entrypoint+"\x00"+a.models[0], b.endpoint+"\x00"+b.entrypoint+"\x00"+b.models[0])
		})
		vd := variantVersionDoc{Version: v}
		for _, g := range gs {
			vd.Rows = append(vd.Rows, variantRowDoc{Endpoint: g.endpoint, Entrypoint: g.entrypoint, Models: g.models, Variants: g.variants})
		}
		doc.Versions = append(doc.Versions, vd)
	}
	identity.SchemaVersion = clientProfileSchemaVersionVariants
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(struct {
		ClientProfile
		Compatibility variantDoc `json:"compatibility"`
	}{identity, doc}); err != nil {
		return nil, err
	}
	if _, err := ParseClientProfile(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("generated profile does not load: %w", err)
	}
	return buf.Bytes(), nil
}
