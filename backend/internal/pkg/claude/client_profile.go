package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync/atomic"

	"golang.org/x/mod/semver"
)

// ClientProfile is one complete, reviewed Claude Code client software identity (#144).
// It is loaded once at startup from a JSON file and every outbound Claude OAuth request
// takes all of its software fields from the same profile, so a CLI version is never paired
// with another release's SDK/runtime values. Betas stay in code (#147); schema_version 2
// added the captured version/model/beta table and schema_version 3 replaced it with exact
// per-request variants (#153). Both load, neither is enforced yet.
type ClientProfile struct {
	SchemaVersion                int                 `json:"schema_version"`
	Revision                     string              `json:"revision"`
	CLIVersion                   string              `json:"cli_version"`
	UserAgent                    string              `json:"user_agent"`
	AnthropicVersion             string              `json:"anthropic_version"`
	StainlessLang                string              `json:"x_stainless_lang"`
	StainlessPackageVersion      string              `json:"x_stainless_package_version"`
	StainlessOS                  string              `json:"x_stainless_os"`
	StainlessArch                string              `json:"x_stainless_arch"`
	StainlessRuntime             string              `json:"x_stainless_runtime"`
	StainlessRuntimeVersion      string              `json:"x_stainless_runtime_version"`
	StainlessTimeout             string              `json:"x_stainless_timeout"`
	XApp                         string              `json:"x_app"`
	DangerousDirectBrowserAccess string              `json:"anthropic_dangerous_direct_browser_access"`
	Source                       ClientProfileSource `json:"source"`

	// compat is built by ParseClientProfile and never mutated, so value copies of the
	// profile can share it; it is reached only through copy-returning lookups.
	compat   *compatibilityTable
	variants *variantTable
}

// ClientProfileSource records where the values came from; it never contains credentials.
type ClientProfileSource struct {
	Artifact       string `json:"artifact"`
	ArtifactDigest string `json:"artifact_digest"`
	CaptureMode    string `json:"capture_mode"`
	CapturedAt     string `json:"captured_at"`
	EvidenceSHA256 string `json:"evidence_sha256"`
	ReviewedBy     string `json:"reviewed_by"`
}

// Schema 1 is the #144 identity alone; schema 2 adds the compatibility table; schema 3
// holds exact per-request variants instead (schema 2 stays loadable until enforcement).
const (
	clientProfileSchemaVersionLegacy   = 1
	clientProfileSchemaVersion         = 2
	clientProfileSchemaVersionVariants = 3
)

// MaxClientProfileBytes bounds the profile file on disk and in release archives.
const MaxClientProfileBytes = 256 << 10

var (
	anthropicVersionRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	sha256HexRe        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var activeClientProfile atomic.Pointer[ClientProfile]

// DefaultClientProfileFile is the shipped location, relative to the working directory.
const DefaultClientProfileFile = "./resources/claude-client-profile/profile.json"

// ClientProfileArchivePath is where release archives carry the shipped profile.
const ClientProfileArchivePath = "resources/claude-client-profile/profile.json"

type clientProfileSource struct {
	path    string
	shipped bool
}

var activeClientProfileSource atomic.Pointer[clientProfileSource]

// SetActiveClientProfileSource records the absolute file the active profile came from and
// whether it is the shipped default location (which release installs may replace).
func SetActiveClientProfileSource(absPath string, shipped bool) {
	activeClientProfileSource.Store(&clientProfileSource{path: absPath, shipped: shipped})
}

// ActiveClientProfileSource returns what SetActiveClientProfileSource recorded.
func ActiveClientProfileSource() (absPath string, shipped bool) {
	if s := activeClientProfileSource.Load(); s != nil {
		return s.path, s.shipped
	}
	return "", false
}

// ActiveClientProfile returns the profile every outbound Claude identity uses. There is
// no built-in fallback: a process that has not loaded a profile is misconfigured, and
// sending some other identity would be worse than failing loudly.
func ActiveClientProfile() ClientProfile {
	if p := activeClientProfile.Load(); p != nil {
		return *p
	}
	panic("claude client profile not loaded: call SetActiveClientProfile at startup (gateway.claude_client_profile_file)")
}

// SetActiveClientProfile installs a validated profile; it is called once at startup.
func SetActiveClientProfile(p ClientProfile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	activeClientProfile.Store(&p)
	return nil
}

// LoadClientProfileFile reads and strictly validates a profile file.
func LoadClientProfileFile(path string) (ClientProfile, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ClientProfile{}, errors.New("claude client profile: file path is empty")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-configured startup path, not request input
	if err != nil {
		return ClientProfile{}, fmt.Errorf("claude client profile: read %s: %w", path, err)
	}
	p, err := ParseClientProfile(raw)
	if err != nil {
		return ClientProfile{}, fmt.Errorf("claude client profile %s: %w", path, err)
	}
	return p, nil
}

// ParseClientProfile decodes exactly one JSON object with no unknown or duplicate keys.
// A schema 1 file loads without a compatibility table; schema 2 and 3 require one.
func ParseClientProfile(raw []byte) (ClientProfile, error) {
	if len(raw) > MaxClientProfileBytes {
		return ClientProfile{}, fmt.Errorf("profile is larger than %d bytes", MaxClientProfileBytes)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return ClientProfile{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc struct {
		ClientProfile
		Compatibility json.RawMessage `json:"compatibility"`
	}
	if err := dec.Decode(&doc); err != nil {
		return ClientProfile{}, fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ClientProfile{}, errors.New("decode: trailing data after the profile object")
	}
	p := doc.ClientProfile
	hasTable := doc.Compatibility != nil && string(doc.Compatibility) != "null"
	switch {
	case p.SchemaVersion == clientProfileSchemaVersionLegacy && hasTable:
		return ClientProfile{}, errors.New("compatibility requires schema_version 2 or 3")
	case p.SchemaVersion != clientProfileSchemaVersionLegacy && !hasTable:
		return ClientProfile{}, fmt.Errorf("schema_version %d requires compatibility", p.SchemaVersion)
	case p.SchemaVersion == clientProfileSchemaVersion:
		var cd compatibilityDoc
		if err := decodeStrict(doc.Compatibility, &cd); err != nil {
			return ClientProfile{}, err
		}
		t, err := buildCompatibilityTable(&cd)
		if err != nil {
			return ClientProfile{}, err
		}
		p.compat = t
	case p.SchemaVersion == clientProfileSchemaVersionVariants:
		var vd variantDoc
		if err := decodeStrict(doc.Compatibility, &vd); err != nil {
			return ClientProfile{}, err
		}
		t, err := buildVariantTable(&vd)
		if err != nil {
			return ClientProfile{}, err
		}
		p.variants = t
	}
	if err := p.Validate(); err != nil {
		return ClientProfile{}, err
	}
	return p, nil
}

// Validate rejects incomplete or inconsistent profiles; messages name the field only.
func (p ClientProfile) Validate() error {
	switch p.SchemaVersion {
	case clientProfileSchemaVersionLegacy:
		if p.compat != nil || p.variants != nil {
			return errors.New("compatibility requires schema_version 2 or 3")
		}
	case clientProfileSchemaVersion:
		if p.compat == nil || p.variants != nil {
			return errors.New("schema_version 2 requires compatibility")
		}
	case clientProfileSchemaVersionVariants:
		if p.variants == nil || p.compat != nil {
			return errors.New("schema_version 3 requires compatibility")
		}
	default:
		return fmt.Errorf("schema_version must be %d, %d or %d", clientProfileSchemaVersionLegacy, clientProfileSchemaVersion, clientProfileSchemaVersionVariants)
	}
	fields := []struct{ name, value string }{
		{"revision", p.Revision}, {"cli_version", p.CLIVersion}, {"user_agent", p.UserAgent},
		{"anthropic_version", p.AnthropicVersion}, {"x_stainless_lang", p.StainlessLang},
		{"x_stainless_package_version", p.StainlessPackageVersion}, {"x_stainless_os", p.StainlessOS},
		{"x_stainless_arch", p.StainlessArch}, {"x_stainless_runtime", p.StainlessRuntime},
		{"x_stainless_runtime_version", p.StainlessRuntimeVersion}, {"x_stainless_timeout", p.StainlessTimeout},
		{"x_app", p.XApp}, {"anthropic_dangerous_direct_browser_access", p.DangerousDirectBrowserAccess},
		{"source.artifact", p.Source.Artifact}, {"source.artifact_digest", p.Source.ArtifactDigest},
		{"source.capture_mode", p.Source.CaptureMode}, {"source.captured_at", p.Source.CapturedAt},
	}
	for _, f := range fields {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("%s is required", f.name)
		}
		if f.value != strings.TrimSpace(f.value) || strings.ContainsAny(f.value, "\r\n\x00") {
			return fmt.Errorf("%s contains surrounding whitespace or control characters", f.name)
		}
	}
	if !isStrictReleaseVersion(p.CLIVersion) {
		return errors.New("cli_version must be a strict three-part release version")
	}
	if p.compat != nil {
		if _, ok := p.compat.versions[p.CLIVersion]; !ok {
			return errors.New("compatibility has no rows for cli_version")
		}
	}
	if p.variants != nil {
		if _, ok := p.variants.versions[p.CLIVersion]; !ok {
			return errors.New("compatibility has no rows for cli_version")
		}
	}
	if p.UserAgent != "claude-cli/"+p.CLIVersion+" (external, cli)" {
		return errors.New("user_agent must be claude-cli/<cli_version> (external, cli)")
	}
	if !anthropicVersionRe.MatchString(p.AnthropicVersion) {
		return errors.New("anthropic_version must be YYYY-MM-DD")
	}
	if !sha256HexRe.MatchString(p.Source.ArtifactDigest) {
		return errors.New("source.artifact_digest must be a lowercase sha256 hex digest")
	}
	if p.Source.EvidenceSHA256 != "" && !sha256HexRe.MatchString(p.Source.EvidenceSHA256) {
		return errors.New("source.evidence_sha256 must be a lowercase sha256 hex digest")
	}
	return nil
}

// decodeStrict decodes one JSON value with no unknown fields.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode compatibility: %w", err)
	}
	return nil
}

// isStrictReleaseVersion accepts X.Y.Z only: no "v" prefix, leading zeros or prerelease.
func isStrictReleaseVersion(v string) bool {
	canonical := "v" + v
	return semver.IsValid(canonical) && semver.Canonical(canonical) == canonical && semver.Prerelease(canonical) == "" && semver.Build(canonical) == ""
}

// rejectDuplicateJSONKeys walks the document and fails on a repeated key in any object.
func rejectDuplicateJSONKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return fmt.Errorf("decode: %w", err)
				}
				key, _ := kt.(string)
				if _, dup := seen[strings.ToLower(key)]; dup {
					return fmt.Errorf("duplicate key %q", key)
				}
				seen[strings.ToLower(key)] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	return walk()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
