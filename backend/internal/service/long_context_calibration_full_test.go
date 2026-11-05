//go:build unit && calibration

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

// These tests prepare the whole calibration corpus through the real Messages preparation
// (about 20s per preparation under -race), which pushed the CI unit run past its 10m
// timeout. The calibration has been run, so they are kept for changes to the tool:
//   go test -race -tags 'unit calibration' ./internal/service -run Calibration

// Dry run with real (stubbed) settings and the production policy shape: every job is
// prepared through Forward, fits the cap and passes the guard; nothing is sent.
func TestRunLongContextCalibrationDry(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	settings := NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{SettingKeyBetaPolicySettings: policyBlockOAuth}}}, cfg)
	k := NewLongContextCalibrator(cfg, nil, settings, &TLSFingerprintProfileService{}, nil)
	account := &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "synthetic-token"}}
	var lines int
	report, err := RunLongContextCalibration(context.Background(), k, account, t.TempDir(), false, func(string, ...any) { lines++ })
	require.NoError(t, err)
	require.Len(t, report.Manifest, 64)
	require.Equal(t, calibrationMaxAttempts+1, lines, "98 jobs plus the manifest line")
	t.Logf("dry manifest_sha256=%s", report.Manifest)
}

// Calibration uses the production identity read-only, and refuses to invent one.
func TestCalibrationIdentityIsReadOnly(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	// The command always has settings; identity rewriting reads them.
	settings := NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{}}}, cfg)
	account := &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "synthetic-token"}}

	missing := &calibrationIdentityStub{}
	k := NewLongContextCalibrator(cfg, nil, settings, &TLSFingerprintProfileService{}, NewReadOnlyIdentityCache(missing))
	_, err := RunLongContextCalibration(context.Background(), k, account, t.TempDir(), false, func(string, ...any) {})
	require.ErrorContains(t, err, "fingerprint")
	require.Zero(t, missing.writes)

	unreadable := &calibrationIdentityStub{readErr: errors.New("private connection details")}
	k = NewLongContextCalibrator(cfg, nil, settings, &TLSFingerprintProfileService{}, NewReadOnlyIdentityCache(unreadable))
	_, err = RunLongContextCalibration(context.Background(), k, account, t.TempDir(), false, func(string, ...any) {})
	require.EqualError(t, err, "identity_cache_read_failed: could not read or decode production fingerprint")
	require.Zero(t, unreadable.writes)

	stored := &calibrationIdentityStub{fp: &Fingerprint{ClientID: "client-id-from-production", UserAgent: "claude-cli/2.1.258 (external, cli)"}}
	k = NewLongContextCalibrator(cfg, nil, settings, &TLSFingerprintProfileService{}, NewReadOnlyIdentityCache(stored))
	wire, err := k.PrepareWireBody(context.Background(), account, []byte(smallMessages))
	require.NoError(t, err)
	req, err := k.buildCountRequest(context.Background(), account, "claude-sonnet-4-6", wire)
	require.NoError(t, err)
	// #144: software fields come from the client profile; the stored fingerprint supplies the ClientID.
	require.Equal(t, claude.DefaultUserAgent(), getHeaderRaw(req.Header, "User-Agent"))
	require.Contains(t, string(wire), "client-id-from-production", "the stored ClientID is used")
	require.Zero(t, stored.writes, "nothing is written to the production cache")
}

// End to end through the real preparation: resume-check verifies the original manifest,
// the count copies and the ledger history, and writes nothing.
func TestRunLongContextCalibrationResumeCheck(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	settings := NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{SettingKeyBetaPolicySettings: policyBlockOAuth}}}, cfg)
	stored := &calibrationIdentityStub{fp: &Fingerprint{ClientID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", UserAgent: "claude-cli/2.1.258 (external, cli)"}}
	k := NewLongContextCalibrator(cfg, nil, settings, &TLSFingerprintProfileService{}, NewReadOnlyIdentityCache(stored))
	account := &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "synthetic-token"}, Extra: map[string]any{"account_uuid": "11111111-2222-3333-4444-555555555555"}}
	dir := t.TempDir()
	freshGatewaySettingsCache()
	plan, err := prepareCalibrationJobs(ctx, k, account)
	require.NoError(t, err)
	oldM := calibrationManifest(71, plan)
	writeOriginalHistory(t, dir+"/ledger.jsonl", plan, nil)
	before, err := os.ReadFile(dir + "/ledger.jsonl")
	require.NoError(t, err)

	_, err = RunLongContextCalibrationResume(ctx, k, account, dir, strings.Repeat("0", 64), "rev", false, func(string, ...any) {})
	require.ErrorContains(t, err, "bodies changed", "a different original manifest is refused")

	freshGatewaySettingsCache()
	report, err := RunLongContextCalibrationResume(ctx, k, account, dir, oldM, "rev", false, func(string, ...any) {})
	require.NoError(t, err)
	require.Equal(t, calibrationManifest(71, calibrationResumeJobs(plan)), report.Manifest)
	after, err := os.ReadFile(dir + "/ledger.jsonl")
	require.NoError(t, err)
	require.Equal(t, before, after, "resume-check writes nothing")
	require.Zero(t, stored.writes)
}

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
