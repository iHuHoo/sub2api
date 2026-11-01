//go:build unit

package service

import (
	"bytes"
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type snapshotFake struct {
	first, earliest  time.Time
	refresh, closed  []time.Time
	sampled          []bool
	lateFrom, lateTo time.Time
	late             []SupplierUsageLateDay
	refreshHook      func(context.Context)
	earliestCalls    int
}

func (f *snapshotFake) RefreshDay(ctx context.Context, d time.Time, s *time.Time) error {
	if f.refreshHook != nil {
		f.refreshHook(ctx)
	}
	f.refresh = append(f.refresh, d)
	f.sampled = append(f.sampled, s != nil)
	return ctx.Err()
}
func (f *snapshotFake) CloseDay(ctx context.Context, d time.Time) error {
	f.closed = append(f.closed, d)
	f.first = d.AddDate(0, 0, 1)
	return nil
}
func (f *snapshotFake) FirstOpenDay(context.Context) (time.Time, bool, error) {
	return f.first, !f.first.IsZero(), nil
}
func (f *snapshotFake) EarliestUsageDay(context.Context) (time.Time, bool, error) {
	f.earliestCalls++
	return f.earliest, !f.earliest.IsZero(), nil
}
func (f *snapshotFake) FindLateUsage(ctx context.Context, a, b time.Time) ([]SupplierUsageLateDay, error) {
	f.lateFrom = a
	f.lateTo = b
	return f.late, ctx.Err()
}

var tickToday = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

func snapshotTestService(f *snapshotFake) *SupplierUsageSnapshotService {
	s := NewSupplierUsageSnapshotService(f, nil, nil, nil, nil)
	s.pause = func(context.Context) error { return nil }
	return s
}
func TestTick_RefreshesTodayWithSampleAndYesterdayWithout(t *testing.T) {
	f := &snapshotFake{}
	require.NoError(t, snapshotTestService(f).tick(context.Background(), tickToday))
	require.Equal(t, []time.Time{tickToday, tickToday.AddDate(0, 0, -1)}, f.refresh)
	require.Equal(t, []bool{true, false}, f.sampled)
}
func TestTick_DoesNotCloseYesterdayInsideGrace(t *testing.T) {
	for _, m := range []int{64, 65} {
		f := &snapshotFake{first: tickToday.AddDate(0, 0, -1)}
		require.NoError(t, snapshotTestService(f).tick(context.Background(), tickToday.Add(time.Duration(m)*time.Minute)))
		require.Len(t, f.closed, m-64)
	}
}
func TestTick_GraceFollowsLiveMaxSession(t *testing.T) {
	for _, tt := range []struct{ seconds, minutes, n int }{{7200, 124, 0}, {7200, 125, 1}, {60, 59, 0}, {60, 60, 1}} {
		f := &snapshotFake{first: tickToday.AddDate(0, 0, -1)}
		cfg := &config.Config{}
		cfg.Gateway.Live.MaxSessionDurationSeconds = tt.seconds
		s := NewSupplierUsageSnapshotService(f, nil, nil, nil, cfg)
		require.NoError(t, s.tick(context.Background(), tickToday.Add(time.Duration(tt.minutes)*time.Minute)))
		require.Len(t, f.closed, tt.n)
	}
}
func TestTick_CatchesUpMissedDaysInOrder(t *testing.T) {
	f := &snapshotFake{first: tickToday.AddDate(0, 0, -3)}
	require.NoError(t, snapshotTestService(f).tick(context.Background(), tickToday.Add(2*time.Hour)))
	require.Equal(t, []time.Time{tickToday.AddDate(0, 0, -3), tickToday.AddDate(0, 0, -2), tickToday.AddDate(0, 0, -1)}, f.closed)
	require.Len(t, f.refresh, 2)
	require.Equal(t, []bool{true, false}, f.sampled)
}
func TestTick_BackfillStartsFromEarliestUsageDay(t *testing.T) {
	f := &snapshotFake{earliest: tickToday.AddDate(0, 0, -3)}
	require.NoError(t, snapshotTestService(f).tick(context.Background(), tickToday.Add(2*time.Hour)))
	require.Len(t, f.closed, 3)
	require.Equal(t, 1, f.earliestCalls)
}
func TestTick_StopsAtBudgetAndResumes(t *testing.T) {
	f := &snapshotFake{first: tickToday.AddDate(0, 0, -3)}
	s := snapshotTestService(f)
	s.budget = 50 * time.Millisecond
	s.pause = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	require.NoError(t, s.tick(context.Background(), tickToday.Add(2*time.Hour)))
	require.Len(t, f.closed, 1)
	require.Equal(t, f.first, f.lateTo)
	require.Equal(t, f.first.AddDate(0, 0, -7), f.lateFrom)
	s.pause = func(context.Context) error { return nil }
	require.NoError(t, s.tick(context.Background(), tickToday.Add(2*time.Hour)))
	require.Len(t, f.closed, 3)
}
func TestTick_ReportsLateUsageWithoutWriting(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	f := &snapshotFake{first: tickToday.AddDate(0, 0, -10), late: []SupplierUsageLateDay{{Day: tickToday.AddDate(0, 0, -11), CostDifference: "1"}}}
	s := snapshotTestService(f)
	s.budget = 50 * time.Millisecond
	s.pause = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	require.NoError(t, s.tick(context.Background(), tickToday))
	require.Len(t, f.closed, 1)
	require.Len(t, f.refresh, 2)
	require.Equal(t, f.first, f.lateTo)
	require.Equal(t, f.first.AddDate(0, 0, -7), f.lateFrom)
	require.Contains(t, logs.String(), "level=ERROR")
	require.Contains(t, logs.String(), "cost_difference=1")
}
func TestSupplierSnapshot_ClosedYesterdayIsNotRefreshed(t *testing.T) {
	f := &snapshotFake{first: tickToday}
	require.NoError(t, snapshotTestService(f).tick(context.Background(), tickToday))
	require.Equal(t, []time.Time{tickToday}, f.refresh)
}
func TestSupplierSnapshot_StopCancelsAndWaits(t *testing.T) {
	started := make(chan struct{})
	f := &snapshotFake{refreshHook: func(ctx context.Context) { close(started); <-ctx.Done() }}
	s := snapshotTestService(f)
	s.Start()
	<-started
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Stop() }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel and wait")
	}
	require.Len(t, f.refresh, 1)
}
func TestSupplierSnapshot_StartIsIdempotent(t *testing.T) {
	entered := make(chan struct{})
	f := &snapshotFake{refreshHook: func(ctx context.Context) { close(entered); <-ctx.Done() }}
	s := snapshotTestService(f)
	var starters sync.WaitGroup
	for n := 0; n < 10; n++ {
		starters.Add(1)
		go func() { defer starters.Done(); s.Start() }()
	}
	starters.Wait()
	<-entered
	s.Stop()
	s.Start()
	require.Len(t, f.refresh, 1)
}

func TestSupplierSnapshot_ConcurrentTicksSkipLocalRun(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := &snapshotFake{refreshHook: func(context.Context) { close(entered); <-release }}
	s := snapshotTestService(f)
	done := make(chan error, 1)
	go func() { done <- s.tick(context.Background(), tickToday) }()
	<-entered
	require.NoError(t, s.tick(context.Background(), tickToday))
	f.refreshHook = nil
	close(release)
	require.NoError(t, <-done)
	require.Len(t, f.refresh, 2)
}
func TestSupplierSnapshot_PeerLockSkipsAndReleases(t *testing.T) {
	cache := &fakeLeaderLockCache{}
	release, ok := tryAcquireSingletonLeaderLock(context.Background(), cache, nil, supplierUsageSnapshotLeaderKey, "peer", time.Hour)
	require.True(t, ok)
	f := &snapshotFake{}
	s := NewSupplierUsageSnapshotService(f, nil, cache, nil, nil)
	require.NoError(t, s.tick(context.Background(), tickToday))
	require.Empty(t, f.refresh)
	release()
	require.NoError(t, s.tick(context.Background(), tickToday))
	require.Len(t, f.refresh, 2)
	require.Empty(t, cache.heldBy(supplierUsageSnapshotLeaderKey))
}

func TestTick_InitialQueriesCannotConsumeAuditReserve(t *testing.T) {
	f := &snapshotFake{first: tickToday.AddDate(0, 0, -3), refreshHook: func(context.Context) { time.Sleep(50 * time.Millisecond) }}
	s := snapshotTestService(f)
	s.budget = 200 * time.Millisecond
	s.pause = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	require.NoError(t, s.tick(context.Background(), tickToday.Add(2*time.Hour)))
	require.Len(t, f.closed, 1)
	require.Equal(t, f.first, f.lateTo, "late audit must execute with its live context after delayed initial queries")
}
