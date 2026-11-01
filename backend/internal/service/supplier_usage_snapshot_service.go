package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
)

const supplierUsageSnapshotLeaderKey = "supplier:usage-snapshot:leader"

type SupplierUsageSnapshotService struct {
	repo          SupplierUsageSnapshotRepository
	timingWheel   *TimingWheelService
	lockCache     LeaderLockCache
	db            *sql.DB
	instanceID    string
	grace, budget time.Duration
	pause         func(context.Context) error
	running       atomic.Bool
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
}

func NewSupplierUsageSnapshotService(repo SupplierUsageSnapshotRepository, timingWheel *TimingWheelService, lockCache LeaderLockCache, db *sql.DB, cfg *config.Config) *SupplierUsageSnapshotService {
	live := time.Hour
	if cfg != nil && cfg.Gateway.Live.MaxSessionDurationSeconds > 0 {
		live = time.Duration(cfg.Gateway.Live.MaxSessionDurationSeconds) * time.Second
	}
	// shortcut: exceptional late writes only alert; revisit before Live billing changes.
	grace := live + 5*time.Minute
	if grace < time.Hour {
		grace = time.Hour
	}
	return &SupplierUsageSnapshotService{repo: repo, timingWheel: timingWheel, lockCache: lockCache, db: db, instanceID: uuid.NewString(), grace: grace, budget: time.Hour, pause: func(ctx context.Context) error {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}}
}

func (s *SupplierUsageSnapshotService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	if s.timingWheel != nil {
		s.timingWheel.ScheduleRecurring(supplierUsageSnapshotLeaderKey, 30*time.Minute, func() { s.run(ctx) })
	}
	s.workers.Add(1)
	go func() { defer s.workers.Done(); s.execute(ctx) }()
}

func (s *SupplierUsageSnapshotService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel == nil {
		return
	}
	s.cancel()
	if s.timingWheel != nil {
		s.timingWheel.Cancel(supplierUsageSnapshotLeaderKey)
	}
	s.workers.Wait()
}

func (s *SupplierUsageSnapshotService) run(ctx context.Context) {
	s.mu.Lock()
	if ctx != s.ctx || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.workers.Add(1)
	s.mu.Unlock()
	defer s.workers.Done()
	s.execute(ctx)
}

func (s *SupplierUsageSnapshotService) execute(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if err := s.tick(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
		slog.Error("supplier usage snapshot failed", "error", err)
	}
}

func (s *SupplierUsageSnapshotService) tick(parent context.Context, now time.Time) error {
	if !s.running.CompareAndSwap(false, true) {
		return nil
	}
	defer s.running.Store(false)
	ctx, cancel := context.WithTimeout(parent, s.budget)
	defer cancel()
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, supplierUsageSnapshotLeaderKey, s.instanceID, 2*time.Hour)
	if !ok {
		return nil
	}
	defer release()
	today := truncateToDayUTC(now)
	if err := s.repo.RefreshDay(ctx, today, &now); err != nil {
		return err
	}
	next, hasClosed, err := s.repo.FirstOpenDay(ctx)
	if err != nil {
		return err
	}
	yesterday := today.AddDate(0, 0, -1)
	if !hasClosed || !yesterday.Before(next) {
		if err := s.repo.RefreshDay(ctx, yesterday, nil); err != nil && !errors.Is(err, ErrSupplierUsageDayClosed) {
			return err
		}
	}
	if !hasClosed {
		next, ok, err = s.repo.EarliestUsageDay(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	// Reserve time for the late-usage audit even when historical backfill exhausts its budget.
	deadline, _ := ctx.Deadline()
	backfill, cancelBackfill := context.WithDeadline(ctx, deadline.Add(-min(time.Minute, s.budget/10)))
	defer cancelBackfill()
	for !next.AddDate(0, 0, 1).Add(s.grace).After(now) {
		if backfill.Err() != nil {
			break
		}
		if err := s.repo.CloseDay(backfill, next); err != nil {
			if backfill.Err() != nil && ctx.Err() == nil {
				break
			}
			return err
		}
		hasClosed = true
		next = next.AddDate(0, 0, 1)
		if next.AddDate(0, 0, 1).Add(s.grace).After(now) {
			break
		}
		if err := s.pause(backfill); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			break
		}
	}
	if !hasClosed {
		return nil
	}
	late, err := s.repo.FindLateUsage(ctx, next.AddDate(0, 0, -7), next)
	if err != nil {
		return err
	}
	for _, day := range late {
		slog.Error("late supplier usage detected", "day", day.Day.Format(time.DateOnly), "cost_difference", day.CostDifference, "request_difference", day.RequestDifference, "input_token_difference", day.InputTokenDifference, "output_token_difference", day.OutputTokenDifference, "cache_creation_token_difference", day.CacheCreationTokenDifference, "cache_read_token_difference", day.CacheReadTokenDifference)
	}
	return nil
}
