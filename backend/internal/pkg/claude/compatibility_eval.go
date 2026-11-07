package claude

import (
	"fmt"
	"slices"
	"strings"
)

// Error classes of a compatibility decision. Every class stops the request locally.
const (
	CompatibilityInvalidIdentity = "invalid_client_identity"
	CompatibilityInvalidModel    = "invalid_client_model"
	CompatibilityInvalidBeta     = "invalid_client_beta"
	CompatibilityInvalidBody     = "invalid_client_body"
	CompatibilityUnsupported     = "unsupported_client_combination" // well-formed but not in the reviewed table
	CompatibilityIncompatible    = "incompatible_client_features"   // the target version cannot carry the source's features
)

// CompatibilityError is a bounded reason: fixed text plus profile values only. It never
// quotes request headers, models, field names, prompts or payload.
type CompatibilityError struct {
	Class  string
	Reason string
}

func (e *CompatibilityError) Error() string { return e.Class + ": " + e.Reason }

// compatErr formats only profile-owned values (cli_version); callers never pass request data.
func compatErr(class, format string, a ...any) error {
	return &CompatibilityError{Class: class, Reason: fmt.Sprintf(format, a...)}
}

// CompatibilitySource is a native Claude Code request as it arrived, before any rewrite.
type CompatibilitySource struct {
	Version     string   // exact version from the client's identity
	Entrypoint  string   // cli | sdk
	Endpoint    string   // messages | count_tokens
	BetaHeaders []string // every anthropic-beta header value received
	Body        []byte   // original body; its model is the source model
}

// CompatibilityResult is what the target request must carry.
type CompatibilityResult struct {
	Version    string
	Endpoint   string
	Entrypoint string
	Model      string
	Betas      []string // target wire order
	Selector   CompatibilitySelector
}

// noCreditDroppedBetas mirrors the existing subscription OAuth no-credit filter (#147): the
// only tokens removed before matching. Nothing else is ever dropped to make a request fit.
var noCreditDroppedBetas = []string{BetaFastMode, BetaContext1M}

// EvaluateNativeCompatibility maps a native request onto the profile's configured target
// version (cli_version): the source must equal a reviewed source variant, and the target
// betas are the target row's reviewed list for the same structural case and the same token
// set. targetModel is the model the gateway will send (after routing). It never adds a
// token, drops one (beyond the no-credit filter) or edits the body.
func (p ClientProfile) EvaluateNativeCompatibility(src CompatibilitySource, targetModel string, noCredit bool) (CompatibilityResult, error) {
	t := p.variants
	if t == nil {
		return CompatibilityResult{}, compatErr(CompatibilityUnsupported, "profile has no schema_version 3 table")
	}
	if !isStrictReleaseVersion(src.Version) {
		return CompatibilityResult{}, compatErr(CompatibilityInvalidIdentity, "source version is not X.Y.Z")
	}
	if err := checkEndpointEntrypoint(src.Endpoint, src.Entrypoint); err != nil {
		return CompatibilityResult{}, err
	}
	if !isModelID(targetModel) {
		return CompatibilityResult{}, compatErr(CompatibilityInvalidModel, "target model is not a model id")
	}
	tokens, err := parseBetaHeaders(src.BetaHeaders, true)
	if err != nil {
		return CompatibilityResult{}, err
	}
	model, sel, err := bodySelector(src.Endpoint, src.Body, "")
	if err != nil {
		return CompatibilityResult{}, err
	}
	if noCredit {
		tokens = slices.DeleteFunc(tokens, func(b string) bool { return slices.Contains(noCreditDroppedBetas, b) })
	}
	srcVocab, ok := t.versions[src.Version]
	if !ok {
		return CompatibilityResult{}, compatErr(CompatibilityUnsupported, "source version is not in the reviewed table")
	}
	for _, b := range tokens {
		if _, ok := srcVocab[b]; !ok {
			return CompatibilityResult{}, compatErr(CompatibilityUnsupported, "a source beta is not in the reviewed table for the source version")
		}
	}
	srcRow, ok := t.rows[compatibilityKey{src.Version, src.Endpoint, src.Entrypoint, model}]
	if !ok {
		return CompatibilityResult{}, compatErr(CompatibilityUnsupported, "the source model has no reviewed row for this version, endpoint and entrypoint")
	}
	if findSet(srcRow.cells[sel], tokens) == nil {
		return CompatibilityResult{}, compatErr(CompatibilityUnsupported, "the source beta combination is not in the reviewed table for this request shape")
	}

	// Reviewed loss: the source carries a token whose semantics the target cannot carry.
	for _, b := range tokens {
		if _, ok := t.versions[p.CLIVersion][b]; !ok && slices.Contains(reviewedUntranslatableBetas, b) {
			return CompatibilityResult{}, compatErr(CompatibilityIncompatible, "the source uses a reviewed feature the target version %s cannot carry", p.CLIVersion)
		}
	}
	dstRow, ok := t.rows[compatibilityKey{p.CLIVersion, src.Endpoint, src.Entrypoint, targetModel}]
	if !ok {
		return CompatibilityResult{}, compatErr(CompatibilityUnsupported, "the target model has no reviewed row for %s on this endpoint and entrypoint", p.CLIVersion)
	}
	// Model-dependent values (effort levels) must also hold for the mapped target model; the
	// body bytes are never rewritten.
	if targetModel != model {
		if _, _, err := bodySelector(src.Endpoint, src.Body, targetModel); err != nil {
			return CompatibilityResult{}, err
		}
	}
	out := findSet(dstRow.cells[sel], tokens)
	if out == nil {
		return CompatibilityResult{}, compatErr(CompatibilityUnsupported, "the beta combination is not in the reviewed table for %s and this request shape", p.CLIVersion)
	}
	return CompatibilityResult{
		Version: p.CLIVersion, Endpoint: src.Endpoint, Entrypoint: src.Entrypoint, Model: targetModel,
		Betas: slices.Clone(out), Selector: sel,
	}, nil
}

// reviewedUntranslatableBetas are tokens reviewed as carrying semantics a target version
// without them cannot preserve (#153: 2.1.280 messages). Any other gap is missing evidence.
var reviewedUntranslatableBetas = []string{BetaPromptCachingScope, BetaMidConversationSystem}

// ValidateTargetCompatibility checks an already-built outbound request (native or mimic)
// against the configured target version: its betas must be exactly, in order, a list the
// target row recorded for the body's structural case. It does not fabricate a source.
func (p ClientProfile) ValidateTargetCompatibility(endpoint, entrypoint string, betaHeaders []string, body []byte) error {
	t := p.variants
	if t == nil {
		return compatErr(CompatibilityUnsupported, "profile has no schema_version 3 table")
	}
	if err := checkEndpointEntrypoint(endpoint, entrypoint); err != nil {
		return err
	}
	tokens, err := parseBetaHeaders(betaHeaders, false)
	if err != nil {
		return err
	}
	model, sel, err := bodySelector(endpoint, body, "")
	if err != nil {
		return err
	}
	row, ok := t.rows[compatibilityKey{p.CLIVersion, endpoint, entrypoint, model}]
	if !ok {
		return compatErr(CompatibilityUnsupported, "the model has no reviewed row for %s on this endpoint and entrypoint", p.CLIVersion)
	}
	if !slices.ContainsFunc(row.cells[sel], func(s []string) bool { return slices.Equal(s, tokens) }) {
		return compatErr(CompatibilityUnsupported, "the betas are not, in this order, in the reviewed table for %s and this request shape", p.CLIVersion)
	}
	return nil
}

func checkEndpointEntrypoint(endpoint, entrypoint string) error {
	if endpoint != CompatibilityEndpointMessages && endpoint != CompatibilityEndpointCountTokens {
		return compatErr(CompatibilityInvalidIdentity, "endpoint is not messages or count_tokens")
	}
	if entrypoint != CompatibilityEntrypointCLI && entrypoint != CompatibilityEntrypointSDK {
		return compatErr(CompatibilityInvalidIdentity, "entrypoint is not cli or sdk")
	}
	return nil
}

// parseBetaHeaders splits every header value on commas and trims tokens. Inbound order and
// repeats carry no meaning, so a source may repeat a token; a built target request may not.
func parseBetaHeaders(values []string, dedupe bool) ([]string, error) {
	var out []string
	for _, v := range values {
		for _, raw := range strings.Split(v, ",") {
			b := strings.TrimSpace(raw)
			if len(b) > 128 || !betaTokenRe.MatchString(b) {
				return nil, compatErr(CompatibilityInvalidBeta, "anthropic-beta holds a malformed token")
			}
			if slices.Contains(out, b) {
				if !dedupe {
					return nil, compatErr(CompatibilityInvalidBeta, "anthropic-beta repeats a token")
				}
				continue
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// isModelID bounds model ids so a reason or lookup never carries request-sized input.
func isModelID(m string) bool { return len(m) <= 128 && modelIDRe.MatchString(m) }

func findSet(sets [][]string, tokens []string) []string {
	for _, s := range sets {
		if sameTokenSet(s, tokens) {
			return s
		}
	}
	return nil
}
