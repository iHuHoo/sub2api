package main

import (
	"log/slog"
	"os"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// legacyClaudeCLIVersionEnv no longer has any effect; the client profile owns the version.
const legacyClaudeCLIVersionEnv = "SUB2API_CLAUDE_CLI_VERSION"

// loadClaudeClientProfile installs the startup Claude client profile (#144). There is no
// fallback: without a valid file the server does not start.
func loadClaudeClientProfile(path string) error {
	p, err := claude.LoadClientProfileFile(path)
	if err != nil {
		return err
	}
	if err := claude.SetActiveClientProfile(p); err != nil {
		return err
	}
	if _, set := os.LookupEnv(legacyClaudeCLIVersionEnv); set {
		slog.Warn("ignoring legacy Claude CLI version override; the client profile owns the version", "env", legacyClaudeCLIVersionEnv)
	}
	slog.Info("claude client profile loaded", "path", path, "revision", p.Revision, "cli_version", p.CLIVersion,
		"package_version", p.StainlessPackageVersion, "runtime_version", p.StainlessRuntimeVersion, "os", p.StainlessOS, "arch", p.StainlessArch)
	return nil
}
