//go:build unit

package service

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCalibrationDiagnosticSavedLoggedAndSingleUse(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	settings := NewSettingService(&longContextSettingRepo{betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: map[string]string{}}}, cfg)
	up := &longContextUpstream{countStatus: 400, countBody: `{"error":{"type":"invalid_request_error","message":"bad field; synthetic-secret proxy-secret"}}`}
	k := NewLongContextCalibrator(cfg, up, settings, &TLSFingerprintProfileService{}, nil)
	a := &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Proxy: &Proxy{Protocol: "http", Host: "proxy.example", Port: 8080, Password: "proxy-secret"}, Credentials: map[string]any{"access_token": "synthetic-secret"}}
	wire, err := k.PrepareWireBody(ctx, a, []byte(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`))
	require.NoError(t, err)
	job := calibrationJob{Model: "claude-sonnet-4-6", Fixture: "readiness", Body: wire, Readiness: true}
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger.jsonl")
	for _, e := range []calibrationLedgerEntry{
		{Phase: "manifest", Manifest: calibrationManifest(71, []calibrationJob{job})},
		{Phase: "dispatch", Seq: 1, Job: job.key(), BodySHA: job.bodySHA()},
		{Phase: "result", Seq: 1, Job: job.key(), BodySHA: job.bodySHA(), Status: 400, ErrClass: "status_400", Terminal: "readiness failed"},
	} {
		require.NoError(t, appendCalibrationLedger(ledger, e))
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	err = RunLongContextReadinessDiagnostic(ctx, k, a, dir)
	require.ErrorContains(t, err, "count status 400")
	require.Equal(t, 1, up.countCalls)
	require.Zero(t, up.inferCalls)
	st, err := readCalibrationLedger(ledger)
	require.NoError(t, err)
	require.Equal(t, 2, st.dispatched)
	require.NotEmpty(t, st.stopped)
	detail := st.results["claude-sonnet-4-6/readiness-diagnostic-1"].Response
	require.NotNil(t, detail)
	require.Contains(t, detail.Body, "bad field")
	for _, text := range []string{detail.Body, logs.String()} {
		require.Contains(t, text, "bad field")
		require.NotContains(t, text, "synthetic-secret")
		require.NotContains(t, text, "proxy-secret")
	}
	require.ErrorContains(t, RunLongContextReadinessDiagnostic(ctx, k, a, dir), "already consumed")
	require.Equal(t, 1, up.countCalls)
}

func TestCalibrationErrorBodyBounds(t *testing.T) {
	r := newCalibrationErrorResponse(&Account{}, &http.Response{StatusCode: 502, Header: http.Header{"Request-Id": {"request-test"}}}, []byte(strings.Repeat("x", (64<<10)+1)), nil)
	require.True(t, r.Truncated)
	require.LessOrEqual(t, len(r.Body), 64<<10)
	require.Equal(t, "request-test", r.RequestID)
}
