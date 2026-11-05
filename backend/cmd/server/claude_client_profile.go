package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// legacyClaudeCLIVersionEnv no longer has any effect; the client profile owns the version.
const legacyClaudeCLIVersionEnv = "SUB2API_CLAUDE_CLI_VERSION"

// loadClaudeClientProfile installs the startup Claude client profile (#144). There is no
// fallback: without a valid file the server does not start.
func loadClaudeClientProfile(path string) error {
	p, err := claude.LoadClientProfileFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && path == claude.DefaultClientProfileFile {
			return fmt.Errorf("%w; the release ships it at %s next to the binary: reinstall with deploy/install.sh upgrade, or copy it from the release archive (an in-app update from a version before #144 does not install it)", err, claude.ClientProfileArchivePath)
		}
		return err
	}
	if err := claude.SetActiveClientProfile(p); err != nil {
		return err
	}
	if abs, err := filepath.Abs(path); err == nil {
		claude.SetActiveClientProfileSource(abs, path == claude.DefaultClientProfileFile)
	}
	if _, set := os.LookupEnv(legacyClaudeCLIVersionEnv); set {
		slog.Warn("ignoring legacy Claude CLI version override; the client profile owns the version", "env", legacyClaudeCLIVersionEnv)
	}
	slog.Info("claude client profile loaded", "path", path, "revision", p.Revision, "cli_version", p.CLIVersion,
		"package_version", p.StainlessPackageVersion, "runtime_version", p.StainlessRuntimeVersion, "os", p.StainlessOS, "arch", p.StainlessArch)
	return nil
}
