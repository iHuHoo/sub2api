//go:build unit

package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// resumeTestPlan is the original 98-job plan shape with tiny bodies: per model a standalone
// readiness probe plus the 48 frozen fixtures with their real names, sets and kinds.
func resumeTestPlan() []calibrationJob {
	var jobs []calibrationJob
	for _, model := range calibrationModels {
		jobs = append(jobs, calibrationJob{Model: model, Fixture: "readiness", Body: []byte(`{"ping":"` + model + `"}`), Readiness: true})
		for _, f := range calibrationCorpus() {
			jobs = append(jobs, calibrationJob{Model: model, Fixture: f.Name, Set: f.Set, Kind: f.Kind,
				Body: []byte(fmt.Sprintf(`{"model":%q,"fixture":%q,"pad":%q}`, model, f.Name, strings.Repeat("x", 100+len(f.Name)*7)))})
		}
	}
	return jobs
}

// writeOriginalHistory reproduces the host ledger: the plan manifest, the Sonnet readiness
// 400 with its stop, and the single authorized diagnostic 400.
func writeOriginalHistory(t *testing.T, ledger string, plan []calibrationJob, mutate func(*[]calibrationLedgerEntry)) {
	t.Helper()
	at := time.Unix(1_800_000_000, 0)
	entries := []calibrationLedgerEntry{
		{Phase: "manifest", Manifest: calibrationManifest(71, plan), AccountID: 71, Budget: calibrationMaxAttempts, At: at},
		{Seq: 1, Phase: "dispatch", Job: "claude-sonnet-4-6/readiness", At: at},
		{Seq: 1, Phase: "result", Job: "claude-sonnet-4-6/readiness", Status: 400, ErrClass: "status_400", Terminal: "readiness call for claude-sonnet-4-6 failed (status_400)", At: at},
		{Phase: "stop", Reason: "readiness call for claude-sonnet-4-6 failed (status_400)", At: at},
		{Seq: 2, Phase: "dispatch", Job: "claude-sonnet-4-6/readiness-diagnostic-1", At: at.Add(time.Minute)},
		{Seq: 2, Phase: "result", Job: "claude-sonnet-4-6/readiness-diagnostic-1", Status: 400, ErrClass: "status_400", Terminal: "authorized diagnostic completed; calibration remains stopped", At: at.Add(time.Minute)},
	}
	if mutate != nil {
		mutate(&entries)
	}
	for _, e := range entries {
		require.NoError(t, appendCalibrationLedger(ledger, e))
	}
}

func okCounter(calls *[]string) calibrationCounter {
	return func(j calibrationJob) (int64, int, bool, error) {
		*calls = append(*calls, j.key())
		return int64(len(j.Body)/4 + 1), 200, false, nil
	}
}

func countResumeEntries(t *testing.T, ledger string) int {
	data, err := os.ReadFile(ledger)
	require.NoError(t, err)
	return strings.Count(string(data), `"phase":"resume"`)
}

func TestCalibrationResumeJobsKeepCorpus(t *testing.T) {
	plan := resumeTestPlan()
	jobs := calibrationResumeJobs(plan)
	require.Len(t, jobs, 96)
	byKey := map[string]calibrationJob{}
	for _, j := range plan {
		byKey[j.key()] = j
	}
	ready := map[string]string{}
	for _, j := range jobs {
		orig := byKey[j.key()]
		require.NotEmpty(t, j.Set, "standalone probes are dropped")
		require.Equal(t, orig.bodySHA(), j.bodySHA())
		require.Equal(t, orig.Set+"/"+orig.Kind, j.Set+"/"+j.Kind)
		if j.Readiness {
			require.Empty(t, ready[j.Model], "one readiness fixture per model")
			ready[j.Model] = j.Fixture
		}
	}
	require.Equal(t, map[string]string{"claude-sonnet-4-6": "prose-0", "claude-opus-4-6": "prose-0"}, ready)
}

func TestCalibrationResumeRunsExactlyTheRemainingBudget(t *testing.T) {
	ledger := t.TempDir() + "/ledger.jsonl"
	plan := resumeTestPlan()
	jobs := calibrationResumeJobs(plan)
	oldM, newM := calibrationManifest(71, plan), calibrationManifest(71, jobs)
	writeOriginalHistory(t, ledger, plan, nil)

	require.NoError(t, checkCalibrationResume(ledger, oldM, newM))
	require.NoError(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
	var calls []string
	require.NoError(t, newCalibrationTestRunner(ledger, okCounter(&calls)).run(jobs))
	require.Len(t, calls, 96)
	st, err := readCalibrationLedger(ledger)
	require.NoError(t, err)
	require.Equal(t, calibrationMaxAttempts, st.dispatched, "2 original + 96 = 98, never more")
	require.Len(t, st.results, 98, "the original evidence is kept")

	report := buildCalibrationReport(jobs, st, newM)
	for _, model := range calibrationModels {
		row := report.Models[model]
		require.Len(t, row.Samples, 48, model)
		split := map[string]int{}
		for _, s := range row.Samples {
			split[s.Set+"/"+s.Kind]++
		}
		require.Equal(t, calibrationExpectedCounts, split, "readiness fixtures are ordinary samples")
		require.Empty(t, row.FitError)
	}

	// Repeating the resume after completion: no transition, no call.
	require.NoError(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
	require.NoError(t, newCalibrationTestRunner(ledger, okCounter(&calls)).run(jobs))
	require.Len(t, calls, 96)
	require.Equal(t, 1, countResumeEntries(t, ledger))
}

func TestCalibrationResumeRejectsUnexpectedHistory(t *testing.T) {
	plan := resumeTestPlan()
	jobs := calibrationResumeJobs(plan)
	oldM, newM := calibrationManifest(71, plan), calibrationManifest(71, jobs)
	for name, mutate := range map[string]func(*[]calibrationLedgerEntry){
		"different status": func(e *[]calibrationLedgerEntry) { (*e)[5].Status = 500 },
		"extra attempt": func(e *[]calibrationLedgerEntry) {
			*e = append(*e, calibrationLedgerEntry{Seq: 3, Phase: "dispatch", Job: "claude-opus-4-6/readiness"},
				calibrationLedgerEntry{Seq: 3, Phase: "result", Job: "claude-opus-4-6/readiness", Status: 400, ErrClass: "status_400"})
		},
		"unresolved dispatch": func(e *[]calibrationLedgerEntry) { *e = (*e)[:5] },
		"other manifest":      func(e *[]calibrationLedgerEntry) { (*e)[0].Manifest = strings.Repeat("0", 64) },
		"diagnostic missing":  func(e *[]calibrationLedgerEntry) { *e = (*e)[:4] },
		"resumed elsewhere": func(e *[]calibrationLedgerEntry) {
			*e = append(*e, calibrationLedgerEntry{Phase: "resume", PrevManifest: (*e)[0].Manifest, Manifest: strings.Repeat("1", 64)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			ledger := t.TempDir() + "/ledger.jsonl"
			writeOriginalHistory(t, ledger, plan, mutate)
			before, err := os.ReadFile(ledger)
			require.NoError(t, err)
			require.Error(t, checkCalibrationResume(ledger, oldM, newM))
			require.Error(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
			after, err := os.ReadFile(ledger)
			require.NoError(t, err)
			require.Equal(t, before, after, "nothing appended")
			var calls []string
			require.Error(t, newCalibrationTestRunner(ledger, okCounter(&calls)).run(jobs))
			require.Empty(t, calls)
		})
	}
	t.Run("without a resume the original stop still holds", func(t *testing.T) {
		ledger := t.TempDir() + "/ledger.jsonl"
		writeOriginalHistory(t, ledger, plan, nil)
		var calls []string
		require.Error(t, newCalibrationTestRunner(ledger, okCounter(&calls)).run(plan), "the old plan stays stopped")
		require.Error(t, newCalibrationTestRunner(ledger, okCounter(&calls)).run(jobs), "the new plan needs the resume entry")
		require.Empty(t, calls)
	})
}

func TestCalibrationResumeInterruptionsAndLaterStops(t *testing.T) {
	plan := resumeTestPlan()
	jobs := calibrationResumeJobs(plan)
	oldM, newM := calibrationManifest(71, plan), calibrationManifest(71, jobs)

	t.Run("interrupted after the transition continues without a second one", func(t *testing.T) {
		ledger := t.TempDir() + "/ledger.jsonl"
		writeOriginalHistory(t, ledger, plan, nil)
		require.NoError(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
		require.NoError(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
		require.Equal(t, 1, countResumeEntries(t, ledger))
		var calls []string
		require.NoError(t, newCalibrationTestRunner(ledger, okCounter(&calls)).run(jobs))
		require.Len(t, calls, 96)
	})
	t.Run("a crash mid-run leaves an unresolved dispatch: refused, not retried", func(t *testing.T) {
		ledger := t.TempDir() + "/ledger.jsonl"
		writeOriginalHistory(t, ledger, plan, nil)
		require.NoError(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
		require.NoError(t, appendCalibrationLedger(ledger, calibrationLedgerEntry{Seq: 3, Phase: "dispatch", Job: jobs[0].key()}))
		require.ErrorContains(t, checkCalibrationResume(ledger, oldM, newM), "no recorded outcome")
		var calls []string
		require.Error(t, newCalibrationTestRunner(ledger, okCounter(&calls)).run(jobs))
		require.Empty(t, calls)
	})
	t.Run("a stop after the resume is permanent", func(t *testing.T) {
		ledger := t.TempDir() + "/ledger.jsonl"
		writeOriginalHistory(t, ledger, plan, nil)
		require.NoError(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
		n := 0
		limited := func(j calibrationJob) (int64, int, bool, error) {
			if n++; n == 3 {
				return 0, 429, false, errors.New("x")
			}
			return 10, 200, false, nil
		}
		require.ErrorIs(t, newCalibrationTestRunner(ledger, limited).run(jobs), errCalibrationStopped)
		require.Equal(t, 3, n)
		require.ErrorContains(t, checkCalibrationResume(ledger, oldM, newM), "stopped after resume")
		require.Error(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
		var calls []string
		require.Error(t, newCalibrationTestRunner(ledger, okCounter(&calls)).run(jobs))
		require.Empty(t, calls)
		require.Equal(t, 1, countResumeEntries(t, ledger))
	})
	t.Run("a failed first fixture stops the run as readiness", func(t *testing.T) {
		ledger := t.TempDir() + "/ledger.jsonl"
		writeOriginalHistory(t, ledger, plan, nil)
		require.NoError(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
		n := 0
		bad := func(calibrationJob) (int64, int, bool, error) { n++; return 0, 400, false, errors.New("x") }
		require.ErrorContains(t, newCalibrationTestRunner(ledger, bad).run(jobs), "readiness")
		require.Equal(t, 1, n)
		st, err := readCalibrationLedger(ledger)
		require.NoError(t, err)
		require.Equal(t, 3, st.dispatched)
	})
	t.Run("a second resume entry in the ledger is corruption", func(t *testing.T) {
		ledger := t.TempDir() + "/ledger.jsonl"
		writeOriginalHistory(t, ledger, plan, nil)
		require.NoError(t, beginCalibrationResume(ledger, oldM, newM, "rev", time.Now()))
		require.NoError(t, appendCalibrationLedger(ledger, calibrationLedgerEntry{Phase: "resume", PrevManifest: newM, Manifest: newM}))
		_, err := readCalibrationLedger(ledger)
		require.ErrorContains(t, err, "resume")
	})
}
