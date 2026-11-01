package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SupplierAccountClassificationSQL expects accounts aliased as a.
const SupplierAccountClassificationSQL = `CASE WHEN a.status = 'error' THEN 'error'
 WHEN a.status = 'active' AND a.schedulable AND NOT a.supplier_paused
 AND EXISTS (SELECT 1 FROM account_groups g WHERE g.account_id=a.id)
 AND (a.parent_account_id IS NULL OR EXISTS (SELECT 1 FROM accounts p WHERE p.id=a.parent_account_id AND p.deleted_at IS NULL AND NOT p.supplier_paused))
 THEN 'enabled' ELSE 'pending' END`

type supplierUsageSnapshotRepository struct {
	db interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
		QueryRowContext(context.Context, string, ...any) *sql.Row
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}
}

func NewSupplierUsageSnapshotRepository(db *sql.DB) service.SupplierUsageSnapshotRepository {
	return &supplierUsageSnapshotRepository{db: db}
}
func supplierUsageUTCDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
func (r *supplierUsageSnapshotRepository) RefreshDay(ctx context.Context, day time.Time, sampleAt *time.Time) error {
	return r.writeDay(ctx, supplierUsageUTCDay(day), sampleAt, false)
}
func (r *supplierUsageSnapshotRepository) CloseDay(ctx context.Context, day time.Time) error {
	return r.writeDay(ctx, supplierUsageUTCDay(day), nil, true)
}
func (r *supplierUsageSnapshotRepository) writeDay(ctx context.Context, day time.Time, sampleAt *time.Time, closeDay bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='60s'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(116249)`); err != nil {
		return err
	}
	var closed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM supplier_usage_day_closes WHERE day=$1)`, day).Scan(&closed); err != nil {
		return err
	}
	if closed {
		if closeDay {
			return nil
		}
		return service.ErrSupplierUsageDayClosed
	}
	if closeDay {
		var next sql.NullTime
		err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT max(day)+1 FROM supplier_usage_day_closes),(SELECT (min(created_at) AT TIME ZONE 'UTC')::date FROM usage_logs))`).Scan(&next)
		if err != nil {
			return err
		}
		if !next.Valid || !next.Time.Equal(day) {
			return service.ErrSupplierUsageDayOutOfOrder
		}
	}
	_, err = tx.ExecContext(ctx, `WITH by_model AS (
 SELECT u.account_id,u.model,count(*) requests,sum(u.input_tokens) input_tokens,sum(u.output_tokens) output_tokens,
 sum(u.cache_creation_tokens) cache_creation_tokens,sum(u.cache_read_tokens) cache_read_tokens,
 sum(u.input_cost::numeric) input_cost,sum(u.output_cost::numeric) output_cost,
 sum(u.cache_creation_cost::numeric) cache_creation_cost,sum(u.cache_read_cost::numeric) cache_read_cost,sum(u.total_cost::numeric) total_cost
 FROM usage_logs u JOIN accounts a ON a.id=u.account_id AND a.supplier_user_id IS NOT NULL
 WHERE u.created_at >= $1 AND u.created_at < $2 GROUP BY u.account_id,u.model
 ), totals AS (
 SELECT account_id,sum(requests) requests,sum(input_tokens) input_tokens,sum(output_tokens) output_tokens,
 sum(cache_creation_tokens) cache_creation_tokens,sum(cache_read_tokens) cache_read_tokens,sum(total_cost) total_cost,
 jsonb_object_agg(model,jsonb_build_object('requests',requests,'input_tokens',input_tokens,'output_tokens',output_tokens,
 'cache_creation_tokens',cache_creation_tokens,'cache_read_tokens',cache_read_tokens,'input_cost',input_cost::text,
 'output_cost',output_cost::text,'cache_creation_cost',cache_creation_cost::text,'cache_read_cost',cache_read_cost::text,'total_cost',total_cost::text)) models
 FROM by_model GROUP BY account_id
 ) INSERT INTO supplier_usage_daily(day,account_id,supplier_user_id,account_name,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,models,enabled,sampled_at,updated_at)
 SELECT ($1::timestamptz AT TIME ZONE 'UTC')::date,a.id,a.supplier_user_id,a.name,COALESCE(t.requests,0),COALESCE(t.input_tokens,0),COALESCE(t.output_tokens,0),COALESCE(t.cache_creation_tokens,0),COALESCE(t.cache_read_tokens,0),COALESCE(t.total_cost,0),COALESCE(t.models,'{}'::jsonb),
 CASE WHEN $3::timestamptz IS NOT NULL AND a.deleted_at IS NULL THEN (`+SupplierAccountClassificationSQL+`)='enabled' ELSE old.enabled END,
 CASE WHEN $3::timestamptz IS NOT NULL AND a.deleted_at IS NULL THEN $3::timestamptz ELSE old.sampled_at END,now()
 FROM accounts a LEFT JOIN totals t ON t.account_id=a.id LEFT JOIN supplier_usage_daily old ON old.account_id=a.id AND old.day=($1::timestamptz AT TIME ZONE 'UTC')::date
 WHERE a.supplier_user_id IS NOT NULL AND (t.account_id IS NOT NULL OR old.account_id IS NOT NULL OR ($3::timestamptz IS NOT NULL AND a.deleted_at IS NULL))
 ON CONFLICT(day,account_id) DO UPDATE SET supplier_user_id=EXCLUDED.supplier_user_id,account_name=EXCLUDED.account_name,requests=EXCLUDED.requests,input_tokens=EXCLUDED.input_tokens,output_tokens=EXCLUDED.output_tokens,cache_creation_tokens=EXCLUDED.cache_creation_tokens,cache_read_tokens=EXCLUDED.cache_read_tokens,total_cost=EXCLUDED.total_cost,models=EXCLUDED.models,enabled=EXCLUDED.enabled,sampled_at=EXCLUDED.sampled_at,updated_at=EXCLUDED.updated_at`, day, day.AddDate(0, 0, 1), sampleAt)
	if err != nil {
		return err
	}
	if closeDay {
		if _, err = tx.ExecContext(ctx, `INSERT INTO supplier_usage_day_closes(day) VALUES($1)`, day); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (r *supplierUsageSnapshotRepository) FirstOpenDay(ctx context.Context) (time.Time, bool, error) {
	var d sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT max(day)+1 FROM supplier_usage_day_closes`).Scan(&d)
	return d.Time.UTC(), d.Valid, err
}
func (r *supplierUsageSnapshotRepository) EarliestUsageDay(ctx context.Context) (time.Time, bool, error) {
	var d sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT (min(created_at) AT TIME ZONE 'UTC')::date FROM usage_logs`).Scan(&d)
	return d.Time.UTC(), d.Valid, err
}
func (r *supplierUsageSnapshotRepository) FindLateUsage(ctx context.Context, from, to time.Time) ([]service.SupplierUsageLateDay, error) {
	rows, err := r.db.QueryContext(ctx, `WITH live AS (
 SELECT (u.created_at AT TIME ZONE 'UTC')::date AS day,u.account_id,sum(u.total_cost::numeric) cost,count(*) requests,sum(u.input_tokens) input_tokens,sum(u.output_tokens) output_tokens,sum(u.cache_creation_tokens) cache_creation_tokens,sum(u.cache_read_tokens) cache_read_tokens
 FROM usage_logs u JOIN accounts a ON a.id=u.account_id AND a.supplier_user_id IS NOT NULL
 WHERE u.created_at >= $1 AND u.created_at < $2 GROUP BY 1,2
 ), differences AS (
 SELECT l.day,greatest(l.cost-COALESCE(s.total_cost,0),0) cost,greatest(l.requests-COALESCE(s.requests,0),0) requests,
 greatest(l.input_tokens-COALESCE(s.input_tokens,0),0) input_tokens,greatest(l.output_tokens-COALESCE(s.output_tokens,0),0) output_tokens,
 greatest(l.cache_creation_tokens-COALESCE(s.cache_creation_tokens,0),0) cache_creation_tokens,greatest(l.cache_read_tokens-COALESCE(s.cache_read_tokens,0),0) cache_read_tokens
 FROM live l JOIN supplier_usage_day_closes c ON c.day=l.day LEFT JOIN supplier_usage_daily s ON s.day=l.day AND s.account_id=l.account_id)
 SELECT day,sum(cost)::text,sum(requests)::bigint,sum(input_tokens)::bigint,sum(output_tokens)::bigint,sum(cache_creation_tokens)::bigint,sum(cache_read_tokens)::bigint FROM differences
 GROUP BY day HAVING sum(cost)>0 OR sum(requests)>0 OR sum(input_tokens)>0 OR sum(output_tokens)>0 OR sum(cache_creation_tokens)>0 OR sum(cache_read_tokens)>0 ORDER BY day`, supplierUsageUTCDay(from), supplierUsageUTCDay(to))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.SupplierUsageLateDay, 0)
	for rows.Next() {
		var d service.SupplierUsageLateDay
		if err = rows.Scan(&d.Day, &d.CostDifference, &d.RequestDifference, &d.InputTokenDifference, &d.OutputTokenDifference, &d.CacheCreationTokenDifference, &d.CacheReadTokenDifference); err != nil {
			return nil, err
		}
		d.Day = d.Day.UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}
