//go:build unit

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalibrationIdentitySnapshotFailures(t *testing.T) {
	ctx := context.Background()
	account := &Account{ID: 71}
	t.Run("read failure does not generate", func(t *testing.T) {
		dir := t.TempDir()
		_, err := NewCalibrationIdentitySnapshot(ctx, &calibrationIdentityStub{readErr: errors.New("private redis details")}, account, dir)
		require.EqualError(t, err, "identity_cache_read_failed: could not read or decode production fingerprint")
		_, err = os.Stat(filepath.Join(dir, "identity.json"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("existing ledger prevents regeneration", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ledger.jsonl"), []byte("ledger"), 0o600))
		_, err := NewCalibrationIdentitySnapshot(ctx, &calibrationIdentityStub{}, account, dir)
		require.ErrorContains(t, err, "identity_snapshot_missing_with_ledger")
	})
	t.Run("corrupt snapshot is not replaced", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "identity.json")
		require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
		_, err := NewCalibrationIdentitySnapshot(ctx, &calibrationIdentityStub{}, account, dir)
		require.ErrorContains(t, err, "identity_snapshot_invalid")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "{", string(data))
	})
	t.Run("reuse stored production fingerprint and bind account", func(t *testing.T) {
		dir := t.TempDir()
		inner := &calibrationIdentityStub{fp: &Fingerprint{ClientID: "existing-client", UserAgent: "claude-cli/2.1.258 (external, cli)"}}
		cache, err := NewCalibrationIdentitySnapshot(ctx, inner, account, dir)
		require.NoError(t, err)
		fp, err := cache.GetFingerprint(ctx, 71)
		require.NoError(t, err)
		require.Equal(t, "existing-client", fp.ClientID)
		require.Zero(t, inner.writes)
		_, err = NewCalibrationIdentitySnapshot(ctx, inner, &Account{ID: 72}, dir)
		require.ErrorContains(t, err, "identity_snapshot_invalid")
	})
}
