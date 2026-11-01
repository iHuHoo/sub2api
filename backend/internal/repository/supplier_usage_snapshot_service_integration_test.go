//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// The wrapper interrupts the actual scheduler after a durable close, without adding production test hooks.
type snapshotRestartRepository struct {
	service.SupplierUsageSnapshotRepository
	closed    chan struct{}
	audit     chan struct{}
	interrupt bool
}

func (r *snapshotRestartRepository) CloseDay(ctx context.Context, day time.Time) error {
	if err := r.SupplierUsageSnapshotRepository.CloseDay(ctx, day); err != nil {
		return err
	}
	if r.interrupt {
		close(r.closed)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (r *snapshotRestartRepository) FindLateUsage(ctx context.Context, from, to time.Time) ([]service.SupplierUsageLateDay, error) {
	rows, err := r.SupplierUsageSnapshotRepository.FindLateUsage(ctx, from, to)
	if err == nil {
		close(r.audit)
	}
	return rows, err
}
func snapshotWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(15 * time.Second):
		t.Fatal("scheduler did not reach expected checkpoint")
	}
}
func TestSupplierUsageSnapshotService_RestartMatchesUninterrupted(t *testing.T) {
	for _, partition := range []bool{false, true} {
		t.Run(fmt.Sprint(partition), func(t *testing.T) {
			f := snapshotSetup(t, partition)
			if partition {
				f.exec(`DROP TABLE usage_logs_oct;CREATE TABLE usage_logs_all PARTITION OF usage_logs DEFAULT`)
			}
			now := time.Now().UTC()
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			// End before yesterday so this test also works inside the configured grace window.
			for i := 4; i >= 2; i-- {
				f.exec(`INSERT INTO usage_logs(account_id,model,total_cost,created_at) VALUES(1,'m',2,$1)`, today.AddDate(0, 0, -i).Add(time.Hour))
			}
			stopAfterClose := &snapshotRestartRepository{SupplierUsageSnapshotRepository: f.r, closed: make(chan struct{}), interrupt: true}
			s := service.NewSupplierUsageSnapshotService(stopAfterClose, nil, nil, nil, nil)
			t.Cleanup(s.Stop)
			s.Start()
			snapshotWait(t, stopAfterClose.closed)
			s.Stop()
			f.check(`SELECT count(*) FROM supplier_usage_day_closes`, 1)
			resumed := &snapshotRestartRepository{SupplierUsageSnapshotRepository: f.r, audit: make(chan struct{})}
			s = service.NewSupplierUsageSnapshotService(resumed, nil, nil, nil, nil)
			t.Cleanup(s.Stop)
			s.Start()
			snapshotWait(t, resumed.audit)
			s.Stop()
			f.exec(`CREATE TABLE resumed AS SELECT * FROM supplier_usage_daily;CREATE TABLE resumed_closes AS SELECT day FROM supplier_usage_day_closes;TRUNCATE supplier_usage_daily,supplier_usage_day_closes`)
			uninterrupted := &snapshotRestartRepository{SupplierUsageSnapshotRepository: f.r, audit: make(chan struct{})}
			s = service.NewSupplierUsageSnapshotService(uninterrupted, nil, nil, nil, nil)
			t.Cleanup(s.Stop)
			s.Start()
			snapshotWait(t, uninterrupted.audit)
			s.Stop()
			f.check(`SELECT NOT EXISTS((SELECT day FROM resumed_closes EXCEPT SELECT day FROM supplier_usage_day_closes) UNION ALL (SELECT day FROM supplier_usage_day_closes EXCEPT SELECT day FROM resumed_closes))`, true)
			f.check(`SELECT NOT EXISTS((SELECT day,account_id,total_cost,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,models,enabled FROM resumed EXCEPT SELECT day,account_id,total_cost,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,models,enabled FROM supplier_usage_daily) UNION ALL (SELECT day,account_id,total_cost,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,models,enabled FROM supplier_usage_daily EXCEPT SELECT day,account_id,total_cost,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,models,enabled FROM resumed))`, true)
			f.check(`SELECT bool_and(sampled_at IS NULL) FROM supplier_usage_daily WHERE day<current_date`, true)
		})
	}
}
