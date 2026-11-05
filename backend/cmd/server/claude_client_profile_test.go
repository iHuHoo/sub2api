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
