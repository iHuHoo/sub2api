package service

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude/claudetest"
	"github.com/stretchr/testify/require"
)

func writeReleaseArchive(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub2api_1.0.0_linux_amd64.tar.gz")
	f, err := os.Create(path)
	require.NoError(t, err)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, f.Close())
	return path
}

// Self-update installs the release's validated profile at the shipped location, keeps the
// previous one for Rollback, and never touches an operator-configured profile (#144).
func TestSelfUpdateInstallsShippedClientProfile(t *testing.T) {
	good, err := os.ReadFile(claudetest.ProfilePath())
	require.NoError(t, err)

	data, err := extractClientProfileFromArchive(writeReleaseArchive(t, map[string][]byte{"sub2api": []byte("bin"), claude.ClientProfileArchivePath: good}))
	require.NoError(t, err)
	require.Equal(t, good, data)
	data, err = extractClientProfileFromArchive(writeReleaseArchive(t, map[string][]byte{"sub2api": []byte("bin")}))
	require.NoError(t, err)
	require.Nil(t, data, "older releases carry no profile")
	_, err = extractClientProfileFromArchive(writeReleaseArchive(t, map[string][]byte{claude.ClientProfileArchivePath: []byte(`{"schema_version":1}`)}))
	require.Error(t, err, "an invalid profile stops the update before anything is replaced")

	prevPath, prevShipped := claude.ActiveClientProfileSource()
	defer claude.SetActiveClientProfileSource(prevPath, prevShipped)
	dir := t.TempDir()
	target := filepath.Join(dir, claude.ClientProfileArchivePath)
	read := func(p string) string {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return string(b)
	}

	// First install with no previous profile: no backup.
	claude.SetActiveClientProfileSource(target, true)
	require.NoError(t, installShippedClientProfile(good))
	require.Equal(t, string(good), read(target))
	require.NoFileExists(t, target+".backup")

	custom := filepath.Join(dir, "custom.json")
	require.NoError(t, os.WriteFile(custom, []byte("mine"), 0o644))
	claude.SetActiveClientProfileSource(custom, false)
	require.NoError(t, installShippedClientProfile(good))
	require.Equal(t, "mine", read(custom), "an operator-configured profile is never replaced")
	require.NoFileExists(t, custom+".backup")
}

func profileWithRevision(t *testing.T, base []byte, revision string) []byte {
	t.Helper()
	out := strings.Replace(string(base), `"revision": "`+claude.ActiveClientProfile().Revision+`"`, `"revision": "`+revision+`"`, 1)
	require.NotEqual(t, string(base), out)
	_, err := claude.ParseClientProfile([]byte(out))
	require.NoError(t, err)
	return []byte(out)
}

// The profile backup always belongs to the binary backup: A->B (profile changes), B->C
// (same profile), B->C' (archive without profile), then rollback restores B's profile,
// never an older one that happened to be left behind.
func TestSelfUpdateProfileBackupFollowsEachTransition(t *testing.T) {
	good, err := os.ReadFile(claudetest.ProfilePath())
	require.NoError(t, err)
	profA, profB := profileWithRevision(t, good, "2026-11-05.8"), profileWithRevision(t, good, "2026-11-05.9")

	prevPath, prevShipped := claude.ActiveClientProfileSource()
	defer claude.SetActiveClientProfileSource(prevPath, prevShipped)
	target := filepath.Join(t.TempDir(), claude.ClientProfileArchivePath)
	claude.SetActiveClientProfileSource(target, true)
	read := func(p string) string {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return string(b)
	}
	require.NoError(t, writeFileAtomic(target, profA)) // release A installed

	require.NoError(t, installShippedClientProfile(profB)) // A -> B
	require.Equal(t, string(profB), read(target))
	require.Equal(t, string(profA), read(target+".backup"))

	require.NoError(t, installShippedClientProfile(profB)) // B -> C, same profile
	require.Equal(t, string(profB), read(target+".backup"), "backup now belongs to binary B")

	require.NoError(t, restoreShippedClientProfileBackup()) // rollback C -> B
	require.Equal(t, string(profB), read(target), "B runs with B's profile, not A's")
	require.NoFileExists(t, target+".backup")

	require.NoError(t, installShippedClientProfile(nil)) // B -> C', archive without profile
	require.Equal(t, string(profB), read(target), "a release without a profile keeps the current one")
	require.Equal(t, string(profB), read(target+".backup"))

	require.NoError(t, os.Remove(target)) // no profile before the update: stale backup removed
	require.NoError(t, installShippedClientProfile(profB))
	require.NoFileExists(t, target+".backup")
}
