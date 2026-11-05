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
// with another release's SDK/runtime values. Betas stay in code (#147).
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

const clientProfileSchemaVersion = 1

var (
	anthropicVersionRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	sha256HexRe        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// builtinClientProfile mirrors backend/resources/claude-client-profile/profile.json
// (a unit test keeps them identical). It serves packages and tests that run without
// startup wiring; the server always loads and validates the file and refuses to start
// when it is missing or invalid.
var builtinClientProfile = ClientProfile{
	SchemaVersion:                1,
	Revision:                     "2026-11-05.1",
	CLIVersion:                   "2.1.318",
	UserAgent:                    "claude-cli/2.1.318 (external, cli)",
	AnthropicVersion:             "2023-06-01",
	StainlessLang:                "js",
	StainlessPackageVersion:      "0.128.0",
	StainlessOS:                  "Linux",
	StainlessArch:                "arm64",
	StainlessRuntime:             "node",
	StainlessRuntimeVersion:      "v26.12.1",
	StainlessTimeout:             "600",
	XApp:                         "cli",
	DangerousDirectBrowserAccess: "true",
	Source: ClientProfileSource{
		Artifact:       "npm @anthropic-ai/claude-code@2.1.318 (native linux-arm64 binary, sha512-OTJgLxcvWefv8IdTsq5n90Z2IxEhLwKqHUBhwZYE4odXzKA/Tb6p2Zb5Iy9Vq7JIVcNZzn9cPxW5sb42wLF95Q==)",
		ArtifactDigest: "53a0d7e219d7b0804ecba5dbc002745e89f786cc946d2de85a6470d5b2d1fb06",
		CaptureMode:    "interactive OAuth (CLAUDE_CODE_OAUTH_TOKEN synthetic), linux/arm64 docker --network none, local receiver via ANTHROPIC_BASE_URL",
		CapturedAt:     "2026-11-05",
		EvidenceSHA256: "b3a9f73417d0dd57b0fd9dc6aa24024cc7f8fac8d8de1105170d9e8f3ecd7283",
		ReviewedBy:     "pending codex review (#144)",
	},
}

var activeClientProfile atomic.Pointer[ClientProfile]

// ActiveClientProfile returns the profile every outbound Claude identity uses.
func ActiveClientProfile() ClientProfile {
	if p := activeClientProfile.Load(); p != nil {
		return *p
	}
	return builtinClientProfile
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
func ParseClientProfile(raw []byte) (ClientProfile, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return ClientProfile{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p ClientProfile
	if err := dec.Decode(&p); err != nil {
		return ClientProfile{}, fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ClientProfile{}, errors.New("decode: trailing data after the profile object")
	}
	if err := p.Validate(); err != nil {
		return ClientProfile{}, err
	}
	return p, nil
}

// Validate rejects incomplete or inconsistent profiles; messages name the field only.
func (p ClientProfile) Validate() error {
	if p.SchemaVersion != clientProfileSchemaVersion {
		return fmt.Errorf("schema_version must be %d", clientProfileSchemaVersion)
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
	canonical := "v" + p.CLIVersion
	if !semver.IsValid(canonical) || semver.Canonical(canonical) != canonical || semver.Prerelease(canonical) != "" {
		return errors.New("cli_version must be a strict three-part release version")
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
