package service

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
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

	claude.SetActiveClientProfileSource(target, true)
	require.NoError(t, installShippedClientProfile(good))
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, good, got)
	require.NoFileExists(t, target+".backup")

	require.NoError(t, os.WriteFile(target, []byte(`{"previous":true}`), 0o644))
	require.NoError(t, installShippedClientProfile(good))
	backup, err := os.ReadFile(target + ".backup")
	require.NoError(t, err)
	require.Equal(t, `{"previous":true}`, string(backup))
	require.NoError(t, restoreShippedClientProfileBackup())
	got, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, `{"previous":true}`, string(got), "rollback restores the previous release's profile")

	custom := filepath.Join(dir, "custom.json")
	require.NoError(t, os.WriteFile(custom, []byte("mine"), 0o644))
	claude.SetActiveClientProfileSource(custom, false)
	require.NoError(t, installShippedClientProfile(good))
	got, err = os.ReadFile(custom)
	require.NoError(t, err)
	require.Equal(t, "mine", string(got), "an operator-configured profile is never replaced")
}
