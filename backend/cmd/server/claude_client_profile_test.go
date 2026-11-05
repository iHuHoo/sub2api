package main

import (
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
