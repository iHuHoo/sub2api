//go:build unit

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCalibrationIdentitySnapshotReuse(t *testing.T) {
	ctx := context.Background()
	account := &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "synthetic"}, Extra: map[string]any{"session_id_masking_enabled": true}}
	dir := t.TempDir()
	inner := &calibrationIdentityStub{}
	cache, err := NewCalibrationIdentitySnapshot(ctx, inner, account, dir)
	require.NoError(t, err)
	fp, err := cache.GetFingerprint(ctx, account.ID)
	require.NoError(t, err)
	require.NotEmpty(t, fp.ClientID)
	session, err := cache.GetMaskedSessionID(ctx, account.ID)
	require.NoError(t, err)
	require.NotEmpty(t, session)
	info, err := os.Stat(filepath.Join(dir, "identity.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	// A restart uses the frozen identity even with Redis unavailable; no random regeneration.
	inner.readErr = errors.New("private redis details")
	again, err := NewCalibrationIdentitySnapshot(ctx, inner, account, dir)
	require.NoError(t, err)
	fp2, err := again.GetFingerprint(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, fp, fp2)
	session2, err := again.GetMaskedSessionID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, session, session2)
	// Caller mutation must not change the frozen snapshot.
	fp2.ClientID = "mutated"
	fp2, err = again.GetFingerprint(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, fp.ClientID, fp2.ClientID)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	settings := NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{}}}, cfg)
	k := NewLongContextCalibrator(cfg, nil, settings, &TLSFingerprintProfileService{}, cache)
	freshGatewaySettingsCache()
	a, err := RunLongContextCalibration(ctx, k, account, dir, false, func(string, ...any) {})
	require.NoError(t, err)
	k = NewLongContextCalibrator(cfg, nil, settings, &TLSFingerprintProfileService{}, again)
	freshGatewaySettingsCache()
	b, err := RunLongContextCalibration(ctx, k, account, dir, false, func(string, ...any) {})
	require.NoError(t, err)
	require.Equal(t, a.Manifest, b.Manifest, "prepared corpus is identical across restart")
	require.Zero(t, inner.writes, "production cache must remain read-only")
}

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
