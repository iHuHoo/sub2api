//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// --- tests -------------------------------------------------------------------------

func TestCalibrationCorpusIsFrozenAndBalanced(t *testing.T) {
	a, b := calibrationCorpus(), calibrationCorpus()
	require.Len(t, a, 48)
	counts := map[string]int{}
	for i := range a {
		require.Equal(t, a[i].SHA256, b[i].SHA256, "deterministic")
		counts[a[i].Set+"/"+a[i].Kind]++
		for _, m := range calibrationModels {
			require.LessOrEqual(t, len(calibrationRequestBody(a[i], m)), calibrationMaxBodyBytes, a[i].Name)
		}
	}
	require.Equal(t, calibrationExpectedCounts, counts)
}

func TestFitCalibrationProfile(t *testing.T) {
	samples := []calibrationSample{
		{"r1", "fitting", "realistic", 1000, 250},
		{"r2", "fitting", "realistic", 1000, 300},
		{"a1", "fitting", "adversarial", 1000, 50},
		{"a2", "fitting", "adversarial", 1000, 400},
		{"h1", "heldout", "realistic", 100_000, 27_000},
		{"h2", "heldout", "adversarial", 100_000, 1_000},
	}
	want := map[string]int{"fitting/realistic": 2, "fitting/adversarial": 2, "heldout/realistic": 1, "heldout/adversarial": 1}
	res, err := fitCalibrationProfile("p", longContextRouteAnthropicOAuth, "m", samples, want)
	require.NoError(t, err)
	// a_low = 0.25/2 = 1/8 (adversarial fitting does not lower it); a_high = 0.4*2 = 4/5.
	require.Equal(t, [4]int64{1, 8, 4, 5}, [4]int64{res.Profile.LowNum, res.Profile.LowDen, res.Profile.HighNum, res.Profile.HighDen})
	require.Empty(t, res.Violations)
	require.Len(t, res.FalseRejections, 1, "compressible adversarial input is a recorded false rejection")

	_, err = fitCalibrationProfile("p", longContextRouteAnthropicOAuth, "m", samples[:5], want)
	require.ErrorContains(t, err, "incomplete", "never fitted on whatever happened to succeed")

	bad := append(append([]calibrationSample(nil), samples...), calibrationSample{"h3", "heldout", "realistic", 1000, 9_999})
	want["heldout/realistic"] = 2
	res, err = fitCalibrationProfile("p", longContextRouteAnthropicOAuth, "m", bad, want)
	require.NoError(t, err)
	require.Len(t, res.Violations, 1, "an upper-bound violation rejects the candidate")

	lo, up := calibrationInterval(res.Profile, 100_000)
	require.Equal(t, longContextAdmit, decideLongContextLocally(100_000, up, res.Profile))
	require.Equal(t, longContextReject, decideLongContextLocally(100_000, lo-1, res.Profile))
	fast, rej := calibrationBands(res.Profile, 1024)
	budget := longContextInputBudget(1024)
	require.Equal(t, longContextAdmit, decideLongContextLocally(int(fast), budget, res.Profile))
	require.NotEqual(t, longContextAdmit, decideLongContextLocally(int(fast+1), budget, res.Profile))
	require.Equal(t, longContextReject, decideLongContextLocally(int(rej), budget, res.Profile))
	require.NotEqual(t, longContextReject, decideLongContextLocally(int(rej-1), budget, res.Profile))
}

type calibrationTestClock struct{ now time.Time }

func (c *calibrationTestClock) Now() time.Time        { return c.now }
func (c *calibrationTestClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

func calibrationTestJobs(n int, prefix string) []calibrationJob {
	var js []calibrationJob
	for i := 0; i < n; i++ {
		js = append(js, calibrationJob{Model: "m", Fixture: fmt.Sprintf("%s%d", prefix, i), Body: []byte(fmt.Sprintf(`{"i":%d}`, i))})
	}
	return js
}

func newCalibrationTestRunner(ledger string, counter calibrationCounter) *calibrationRunner {
	clock := &calibrationTestClock{now: time.Unix(1_800_000_000, 0)}
	return &calibrationRunner{LedgerPath: ledger, AccountID: 71, Counter: counter, Now: clock.Now, Sleep: clock.Sleep}
}

func TestCalibrationRunnerBudgetPacingAndResume(t *testing.T) {
	ledger := t.TempDir() + "/ledger.jsonl"
	calls := 0
	ok := func(calibrationJob) (int64, int, bool, error) { calls++; return 10, 200, false, nil }
	jobs := append(calibrationTestJobs(60, "f"), calibrationTestJobs(60, "g")...)

	clock := &calibrationTestClock{now: time.Unix(1_800_000_000, 0)}
	slept := time.Duration(0)
	r := &calibrationRunner{LedgerPath: ledger, AccountID: 71, Counter: ok, Now: clock.Now, Sleep: func(d time.Duration) { slept += d; clock.Sleep(d) }}
	err := r.run(jobs)
	require.ErrorIs(t, err, errCalibrationStopped)
	require.Equal(t, calibrationMaxAttempts, calls)
	require.Equal(t, time.Duration(calibrationMaxAttempts-1)*calibrationMinInterval, slept)

	// A restart neither resets the budget nor retries anything.
	err = newCalibrationTestRunner(ledger, ok).run(jobs)
	require.ErrorIs(t, err, errCalibrationStopped)
	require.Equal(t, calibrationMaxAttempts, calls)
	st, err := readCalibrationLedger(ledger)
	require.NoError(t, err)
	require.Equal(t, calibrationMaxAttempts, st.dispatched)
	require.Contains(t, st.stopped, "budget")
}

func TestCalibrationRunnerStopsAreDurable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		counter calibrationCounter
		calls   int
	}{
		{"429", func(calibrationJob) (int64, int, bool, error) { return 0, 429, false, errors.New("x") }, 1},
		{"401", func(calibrationJob) (int64, int, bool, error) { return 0, 401, false, errors.New("x") }, 1},
		{"redirect", func(calibrationJob) (int64, int, bool, error) { return 0, 307, false, errors.New("x") }, 1},
		{"refused by preflight", func(calibrationJob) (int64, int, bool, error) { return 0, 0, false, errors.New("guard") }, 1},
		{"two transport failures", func(calibrationJob) (int64, int, bool, error) { return 0, 0, true, errors.New("x") }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := t.TempDir() + "/l.jsonl"
			calls := 0
			counter := func(j calibrationJob) (int64, int, bool, error) { calls++; return tc.counter(j) }
			require.ErrorIs(t, newCalibrationTestRunner(ledger, counter).run(calibrationTestJobs(5, "f")), errCalibrationStopped)
			require.Equal(t, tc.calls, calls)
			err := newCalibrationTestRunner(ledger, counter).run(calibrationTestJobs(5, "f"))
			require.ErrorContains(t, err, "previously stopped")
			require.Equal(t, tc.calls, calls, "restart makes no call")
		})
	}
	t.Run("readiness failure", func(t *testing.T) {
		ledger := t.TempDir() + "/l.jsonl"
		calls := 0
		js := calibrationTestJobs(3, "f")
		js[0].Readiness = true
		counter := func(calibrationJob) (int64, int, bool, error) { calls++; return 0, 500, false, errors.New("x") }
		require.ErrorIs(t, newCalibrationTestRunner(ledger, counter).run(js), errCalibrationStopped)
		require.ErrorContains(t, newCalibrationTestRunner(ledger, counter).run(js), "previously stopped")
		require.Equal(t, 1, calls)
	})
}

func TestCalibrationRunnerResumeSafety(t *testing.T) {
	t.Run("a failed job is never retried and the transport streak survives restart", func(t *testing.T) {
		ledger := t.TempDir() + "/l.jsonl"
		var seq []string
		first := func(j calibrationJob) (int64, int, bool, error) {
			seq = append(seq, j.Fixture)
			if j.Fixture == "f1" {
				return 0, 0, true, errors.New("reset")
			}
			return 10, 200, false, nil
		}
		jobs := calibrationTestJobs(2, "f")
		require.NoError(t, newCalibrationTestRunner(ledger, first).run(jobs))
		more := append(jobs, calibrationTestJobs(2, "g")...)
		// Manifest changed (more jobs): refused, no call.
		require.ErrorContains(t, newCalibrationTestRunner(ledger, first).run(more), "different manifest")
		// Same manifest: f1 (failed) is not retried, nothing left to do.
		require.NoError(t, newCalibrationTestRunner(ledger, first).run(jobs))
		require.Equal(t, []string{"f0", "f1"}, seq)
		st, err := readCalibrationLedger(ledger)
		require.NoError(t, err)
		require.Equal(t, 1, st.trailingTransport)
	})
	t.Run("changed fixture bytes under the same name are refused", func(t *testing.T) {
		ledger := t.TempDir() + "/l.jsonl"
		ok := func(calibrationJob) (int64, int, bool, error) { return 10, 200, false, nil }
		jobs := calibrationTestJobs(2, "f")
		require.NoError(t, newCalibrationTestRunner(ledger, ok).run(jobs))
		jobs[1].Body = []byte(`{"changed":true}`)
		require.ErrorContains(t, newCalibrationTestRunner(ledger, ok).run(jobs), "different manifest")
	})
	t.Run("a dispatch without a recorded outcome (crash) refuses to run", func(t *testing.T) {
		ledger := t.TempDir() + "/l.jsonl"
		jobs := calibrationTestJobs(3, "f")
		require.NoError(t, appendCalibrationLedger(ledger, calibrationLedgerEntry{Phase: "manifest", Manifest: calibrationManifest(71, jobs)}))
		require.NoError(t, appendCalibrationLedger(ledger, calibrationLedgerEntry{Seq: 1, Phase: "dispatch", Job: jobs[0].key()}))
		calls := 0
		err := newCalibrationTestRunner(ledger, func(calibrationJob) (int64, int, bool, error) { calls++; return 1, 200, false, nil }).run(jobs)
		require.ErrorContains(t, err, "no recorded outcome")
		require.Zero(t, calls)
	})
	t.Run("corrupt ledger and a held lock refuse to run", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/l.jsonl", []byte("{not json\n"), 0o600))
		require.ErrorContains(t, newCalibrationTestRunner(dir+"/l.jsonl", nil).run(calibrationTestJobs(1, "f")), "corrupt")
		require.NoError(t, os.WriteFile(dir+"/m.jsonl.lock", nil, 0o600))
		require.ErrorContains(t, newCalibrationTestRunner(dir+"/m.jsonl", nil).run(calibrationTestJobs(1, "f")), "locked")
	})
}

// The guard must recognise the builder's raw lowercase headers, and still refuse a wrong
// destination or an API key. Built offline with a synthetic credential; nothing is sent.
func TestCalibrationCountRequestGuard(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	k := NewLongContextCalibrator(cfg, nil, nil, &TLSFingerprintProfileService{}, nil)
	account := &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "synthetic-token"}}
	wire, err := k.PrepareWireBody(context.Background(), account, []byte(smallMessages))
	require.NoError(t, err)
	req, err := k.buildCountRequest(context.Background(), account, "claude-sonnet-4-6", wire)
	require.NoError(t, err)
	require.Equal(t, "Bearer synthetic-token", getHeaderRaw(req.Header, "authorization"))

	wrongHost := req.Clone(context.Background())
	wrongHost.URL.Host = "relay.example"
	require.Error(t, checkCalibrationCountRequest(wrongHost))
	withKey := req.Clone(context.Background())
	setHeaderRaw(withKey.Header, "x-api-key", "k")
	require.Error(t, checkCalibrationCountRequest(withKey))
	noBearer := req.Clone(context.Background())
	deleteHeaderAllForms(noBearer.Header, "authorization")
	require.Error(t, checkCalibrationCountRequest(noBearer))
	require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()), "count requests never follow redirects")
}

func TestCheckLongContextCalibrationAccount(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	good := func() *Account {
		return &Account{ID: 71, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Proxy: &Proxy{Protocol: "http", Host: "proxy.example", Port: 8080}, Credentials: map[string]any{"access_token": "t"}}
	}
	require.NoError(t, CheckLongContextCalibrationAccount(good(), 71, now))
	other := good()
	other.ID = 72
	require.Error(t, CheckLongContextCalibrationAccount(other, 71, now))
	noProxy := good()
	noProxy.Proxy = nil
	require.ErrorContains(t, CheckLongContextCalibrationAccount(noProxy, 71, now), "proxy")
	paused := good()
	paused.Schedulable = false
	require.Error(t, CheckLongContextCalibrationAccount(paused, 71, now))
	expired := good()
	expired.Credentials["expires_at"] = now.Add(-time.Hour).Format(time.RFC3339)
	require.ErrorContains(t, CheckLongContextCalibrationAccount(expired, 71, now), "expired")
}

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

// A terminal result is durable on its own: with the separate stop row missing (crash or a
// failed write), a resumed run still makes no further call. A non-terminal failure does not
// stop the run and is not retried.
func TestCalibrationTerminalResultWithoutStopRow(t *testing.T) {
	jobs := calibrationTestJobs(4, "f")
	ready := append([]calibrationJob(nil), jobs...)
	ready[0].Readiness = true
	for _, tc := range []struct {
		name    string
		jobs    []calibrationJob
		results []calibrationLedgerEntry
		calls   int
	}{
		{"429", jobs, []calibrationLedgerEntry{{Status: 429, ErrClass: "status_429", Terminal: "429"}}, 0},
		{"401", jobs, []calibrationLedgerEntry{{Status: 401, ErrClass: "status_401", Terminal: "401"}}, 0},
		{"403", jobs, []calibrationLedgerEntry{{Status: 403, ErrClass: "status_403", Terminal: "403"}}, 0},
		{"302", jobs, []calibrationLedgerEntry{{Status: 302, ErrClass: "status_302", Terminal: "302"}}, 0},
		{"preflight refusal", jobs, []calibrationLedgerEntry{{ErrClass: "status_0", Terminal: "refused"}}, 0},
		{"readiness 500", ready, []calibrationLedgerEntry{{Status: 500, ErrClass: "status_500", Terminal: "readiness"}}, 0},
		// Older ledgers without the terminal field: two transport failures still stop.
		{"two transport failures", jobs, []calibrationLedgerEntry{{ErrClass: "transport"}, {ErrClass: "transport"}}, 0},
		{"non-terminal 400 continues", jobs, []calibrationLedgerEntry{{Status: 400, ErrClass: "status_400"}}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := t.TempDir() + "/l.jsonl"
			require.NoError(t, appendCalibrationLedger(ledger, calibrationLedgerEntry{Phase: "manifest", Manifest: calibrationManifest(71, tc.jobs)}))
			for i, r := range tc.results {
				j := tc.jobs[i]
				require.NoError(t, appendCalibrationLedger(ledger, calibrationLedgerEntry{Seq: i + 1, Phase: "dispatch", Job: j.key()}))
				r.Seq, r.Phase, r.Job = i+1, "result", j.key()
				require.NoError(t, appendCalibrationLedger(ledger, r))
			}
			var called []string
			err := newCalibrationTestRunner(ledger, func(j calibrationJob) (int64, int, bool, error) {
				called = append(called, j.Fixture)
				return 1, 200, false, nil
			}).run(tc.jobs)
			require.Len(t, called, tc.calls, "%v", called)
			if tc.calls == 0 {
				require.ErrorContains(t, err, "previously stopped")
			} else {
				require.NoError(t, err)
				require.NotContains(t, called, tc.jobs[0].Fixture, "a failed job is never retried")
			}
		})
	}
	t.Run("the runner writes the terminal reason into the result itself", func(t *testing.T) {
		ledger := t.TempDir() + "/l.jsonl"
		_ = newCalibrationTestRunner(ledger, func(calibrationJob) (int64, int, bool, error) { return 0, 429, false, errors.New("x") }).run(jobs)
		st, err := readCalibrationLedger(ledger)
		require.NoError(t, err)
		require.NotEmpty(t, st.results[jobs[0].key()].Terminal)
	})
}

type calibrationIdentityStub struct {
	fp     *Fingerprint
	writes int
}

func (c *calibrationIdentityStub) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	return c.fp, nil
}
func (c *calibrationIdentityStub) SetFingerprint(context.Context, int64, *Fingerprint) error {
	c.writes++
	return nil
}
func (c *calibrationIdentityStub) GetMaskedSessionID(context.Context, int64) (string, error) {
	return "", nil
}
func (c *calibrationIdentityStub) SetMaskedSessionID(context.Context, int64, string) error {
	c.writes++
	return nil
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

	stored := &calibrationIdentityStub{fp: &Fingerprint{ClientID: "client-id-from-production", UserAgent: "claude-cli/2.1.258 (external, cli)"}}
	k = NewLongContextCalibrator(cfg, nil, settings, &TLSFingerprintProfileService{}, NewReadOnlyIdentityCache(stored))
	wire, err := k.PrepareWireBody(context.Background(), account, []byte(smallMessages))
	require.NoError(t, err)
	req, err := k.buildCountRequest(context.Background(), account, "claude-sonnet-4-6", wire)
	require.NoError(t, err)
	require.Equal(t, "claude-cli/2.1.258 (external, cli)", getHeaderRaw(req.Header, "User-Agent"), "the stored fingerprint is applied")
	require.Zero(t, stored.writes, "nothing is written to the production cache")
}
