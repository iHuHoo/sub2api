package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

// Startup accepts the shipped file from a copied resources directory (no repository
// paths) and refuses missing, partial and invalid files.
func TestLoadClaudeClientProfileAtStartup(t *testing.T) {
	good, err := os.ReadFile("../../resources/claude-client-profile/profile.json")
	require.NoError(t, err)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, b, 0o600))
		return p
	}
	require.NoError(t, loadClaudeClientProfile(write("profile.json", good)))
	require.Equal(t, "2.1.318", claude.ActiveClientProfile().CLIVersion)

	require.Error(t, loadClaudeClientProfile(filepath.Join(dir, "missing.json")))
	require.Error(t, loadClaudeClientProfile(write("partial.json", good[:len(good)/2])))
	require.Error(t, loadClaudeClientProfile(write("bad.json", []byte(`{"schema_version":1}`))))
	require.Error(t, loadClaudeClientProfile(""))
}

// A schema 1 file (the #144 release file or an operator's copy) still starts the server,
// with no compatibility table and a warning; the shipped schema 2 file carries the table.
func TestLoadClaudeClientProfileAcceptsLegacySchema(t *testing.T) {
	legacy, err := os.ReadFile("../../internal/pkg/claude/testdata/profile-schema1.json")
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	require.NoError(t, os.WriteFile(path, legacy, 0o600))

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	require.NoError(t, loadClaudeClientProfile(path))
	require.Equal(t, 1, claude.ActiveClientProfile().SchemaVersion)
	require.False(t, claude.ActiveClientProfile().HasCompatibility())
	require.Contains(t, logs.String(), "no compatibility table")
	require.Contains(t, logs.String(), "compatibility_enforced=false")

	logs.Reset()
	require.NoError(t, loadClaudeClientProfile("../../resources/claude-client-profile/profile.json"))
	require.True(t, claude.ActiveClientProfile().HasCompatibility())
	require.NotContains(t, logs.String(), "no compatibility table")
	require.Contains(t, logs.String(), "compatibility_enforced=false")
}

// An extracted release or /opt/sub2api install: the default relative path resolves from a
// working directory outside the repository, and the source is recorded as shipped.
func TestLoadClaudeClientProfileFromInstalledLayout(t *testing.T) {
	good, err := os.ReadFile("../../resources/claude-client-profile/profile.json")
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "resources", "claude-client-profile"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, claude.ClientProfileArchivePath), good, 0o644))
	t.Chdir(root)

	require.NoError(t, loadClaudeClientProfile(claude.DefaultClientProfileFile))
	path, shipped := claude.ActiveClientProfileSource()
	require.True(t, shipped)
	require.Equal(t, resolved(t, filepath.Join(root, claude.ClientProfileArchivePath)), resolved(t, path))

	empty := t.TempDir()
	t.Chdir(empty)
	err = loadClaudeClientProfile(claude.DefaultClientProfileFile)
	require.ErrorContains(t, err, "install.sh upgrade", "a missing shipped profile says how to fix it")
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}
