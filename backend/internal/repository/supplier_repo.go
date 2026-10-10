package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/proxy"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type supplierRepository struct {
	client *dbent.Client
	sql    sqlExecutor
	cache  service.SchedulerCache
}

func NewSupplierRepository(client *dbent.Client, db *sql.DB, cache service.SchedulerCache) service.SupplierRepository {
	return newSupplierRepository(client, db, cache)
}
func newSupplierRepository(client *dbent.Client, exec sqlExecutor, cache service.SchedulerCache) *supplierRepository {
	return &supplierRepository{client: client, sql: exec, cache: cache}
}

func (r *supplierRepository) ListAccounts(ctx context.Context, owner int64, p pagination.PaginationParams) ([]service.Account, int64, error) {
	q := r.client.Account.Query().Where(account.SupplierUserIDEQ(owner))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(dbent.Desc(account.FieldID)).Offset(p.Offset()).Limit(p.Limit()).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out, err := r.accountRows(ctx, owner, rows)
	return out, int64(total), err
}
func (r *supplierRepository) accountRows(ctx context.Context, owner int64, rows []*dbent.Account) ([]service.Account, error) {
	out := make([]service.Account, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.ProxyID != nil {
			ids = append(ids, *row.ProxyID)
		}
	}
	proxies := map[int64]*service.Proxy{}
	if len(ids) > 0 {
		ps, err := r.client.Proxy.Query().Where(proxy.IDIn(ids...), proxy.SupplierUserIDEQ(owner)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			proxies[p.ID] = proxyEntityToService(p)
		}
	}
	for _, row := range rows {
		a := accountEntityToService(row)
		if row.ProxyID != nil {
			a.Proxy = proxies[*row.ProxyID]
		}
		out = append(out, *a)
	}
	return out, nil
}
func (r *supplierRepository) GetAccount(ctx context.Context, owner, id int64) (*service.Account, error) {
	row, err := r.client.Account.Query().Where(account.IDEQ(id), account.SupplierUserIDEQ(owner)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, service.ErrSupplierUnavailable
	}
	if err != nil {
		return nil, err
	}
	out, err := r.accountRows(ctx, owner, []*dbent.Account{row})
	if err != nil {
		return nil, err
	}
	return &out[0], nil
}

func (r *supplierRepository) transaction(ctx context.Context, fn func(*supplierRepository) error) (err error) {
	defer func() {
		var constraint *pq.Error
		if errors.As(err, &constraint) && constraint.Code == "23503" && constraint.Constraint == "live_proxy_reference" {
			err = service.ErrSupplierUnavailable
		}
	}()
	tx, err := r.client.Tx(ctx)
	if errors.Is(err, dbent.ErrTxStarted) {
		return fn(r)
	}
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	tr := newSupplierRepository(tx.Client(), tx, nil)
	if err = fn(tr); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *supplierRepository) lockAccount(ctx context.Context, owner, id int64) error {
	rows, err := r.sql.QueryContext(ctx, `SELECT parent_account_id FROM accounts WHERE id=$1 AND supplier_user_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, owner)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return service.ErrSupplierUnavailable
	}
	var parent sql.NullInt64
	if err = rows.Scan(&parent); err != nil {
		return err
	}
	if parent.Valid {
		return service.ErrSupplierReadOnly
	}
	return rows.Err()
}
func (r *supplierRepository) lockProxy(ctx context.Context, owner, id int64) error {
	rows, err := r.sql.QueryContext(ctx, `SELECT id FROM proxies WHERE id=$1 AND supplier_user_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, owner)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return service.ErrSupplierUnavailable
	}
	return rows.Err()
}
func (r *supplierRepository) ownedProxyBinding(ctx context.Context, owner int64, id *int64) error {
	if id == nil || *id == 0 {
		return nil
	}
	if *id < 0 {
		return service.ErrSupplierInvalid
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT id FROM proxies WHERE id=$1 AND supplier_user_id=$2 AND deleted_at IS NULL FOR SHARE`, *id, owner)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return service.ErrSupplierUnavailable
	}
	return rows.Err()
}
func (r *supplierRepository) CreateAccount(ctx context.Context, owner int64, a *service.Account) error {
	a.SupplierUserID = &owner
	return r.transaction(ctx, func(tr *supplierRepository) error {
		if err := tr.ownedProxyBinding(ctx, owner, a.ProxyID); err != nil {
			return err
		}
		if a.ProxyID != nil && *a.ProxyID == 0 {
			a.ProxyID = nil
		}
		ar := newAccountRepositoryWithSQL(tr.client, tr.sql, nil)
		return ar.CreateWithAccountGroups(ctx, a, nil)
	})
}

func (r *supplierRepository) accountChanges(ctx context.Context, id int64) ([]int64, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT id FROM accounts WHERE (id=$1 OR parent_account_id=$1) AND deleted_at IS NULL ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var v int64
		if err = rows.Scan(&v); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, v)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if err = enqueueProxyProbeAccountChanges(ctx, r.sql, ids); err != nil {
		return nil, err
	}
	return ids, nil
}
func (r *supplierRepository) refresh(ctx context.Context, ids []int64) {
	ar := newAccountRepositoryWithSQL(r.client, r.sql, r.cache)
	for _, id := range ids {
		ar.syncSchedulerAccountSnapshotDetached(ctx, id)
	}
}

func (r *supplierRepository) UpdateAccount(ctx context.Context, owner, id int64, in service.SupplierAccountUpdate) (*service.Account, error) {
	var changed []int64
	err := r.transaction(ctx, func(tr *supplierRepository) error {
		if err := tr.lockAccount(ctx, owner, id); err != nil {
			return err
		}
		if err := tr.ownedProxyBinding(ctx, owner, in.ProxyID); err != nil {
			return err
		}
		sets := []string{"updated_at=NOW()"}
		args := []any{id, owner}
		add := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s=$%d", col, len(args)))
		}
		if in.Name != nil {
			add("name", strings.TrimSpace(*in.Name))
		}
		if in.Notes != nil {
			add("supplier_notes", strings.TrimSpace(*in.Notes))
		}
		if in.Credentials != nil {
			raw, err := json.Marshal(in.Credentials)
			if err != nil {
				return service.ErrSupplierInvalid
			}
			args = append(args, string(raw))
			sets = append(sets, fmt.Sprintf("credentials=COALESCE(credentials,'{}'::jsonb)||$%d::jsonb||jsonb_build_object('_token_version',GREATEST(COALESCE((credentials->>'_token_version')::bigint,0)+1,(EXTRACT(EPOCH FROM clock_timestamp())*1000)::bigint))", len(args)))
		}
		if in.ProxyID != nil {
			if *in.ProxyID == 0 {
				add("proxy_id", nil)
			} else {
				add("proxy_id", *in.ProxyID)
			}
		}
		if in.ExpiresAt != nil {
			if *in.ExpiresAt == 0 {
				add("expires_at", nil)
			} else {
				add("expires_at", time.Unix(*in.ExpiresAt, 0))
			}
		}
		if in.Credentials != nil || in.ProxyID != nil {
			sets = append(sets, `extra=COALESCE(extra,'{}'::jsonb)-'upstream_billing_probe'-'ollama_cloud_usage_snapshot'-'opencode_go_usage_snapshot'`)
		}
		if _, err := tr.sql.ExecContext(ctx, "UPDATE accounts SET "+strings.Join(sets, ",")+" WHERE id=$1 AND supplier_user_id=$2 AND deleted_at IS NULL", args...); err != nil {
			return err
		}
		if in.ProxyID != nil {
			var binding any
			if *in.ProxyID != 0 {
				binding = *in.ProxyID
			}
			// Credentials are inherited from the parent; the gateway uses the selected
			// shadow's proxy. Update only that binding, preserving administrator fields.
			if _, err := tr.sql.ExecContext(ctx, `UPDATE accounts SET proxy_id=$3,updated_at=NOW(),extra=COALESCE(extra,'{}'::jsonb)-'upstream_billing_probe'-'ollama_cloud_usage_snapshot'-'opencode_go_usage_snapshot' WHERE parent_account_id=$1 AND supplier_user_id=$2 AND deleted_at IS NULL`, id, owner, binding); err != nil {
				return err
			}
		}
		var err error
		changed, err = tr.accountChanges(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	r.refresh(ctx, changed)
	return r.GetAccount(ctx, owner, id)
}
func (r *supplierRepository) PauseAccount(ctx context.Context, owner, id int64, paused bool) (*service.Account, error) {
	var changed []int64
	err := r.transaction(ctx, func(tr *supplierRepository) error {
		if err := tr.lockAccount(ctx, owner, id); err != nil {
			return err
		}
		if _, err := tr.sql.ExecContext(ctx, `UPDATE accounts SET supplier_paused=$3,updated_at=NOW() WHERE id=$1 AND supplier_user_id=$2 AND deleted_at IS NULL`, id, owner, paused); err != nil {
			return err
		}
		var err error
		changed, err = tr.accountChanges(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	r.refresh(ctx, changed)
	return r.GetAccount(ctx, owner, id)
}
func (r *supplierRepository) ListProxies(ctx context.Context, owner int64, p pagination.PaginationParams) ([]service.Proxy, int64, error) {
	q := r.client.Proxy.Query().Where(proxy.SupplierUserIDEQ(owner))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(dbent.Desc(proxy.FieldID)).Offset(p.Offset()).Limit(p.Limit()).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]service.Proxy, 0, len(rows))
	for _, row := range rows {
		out = append(out, *proxyEntityToService(row))
	}
	return out, int64(total), nil
}
func (r *supplierRepository) GetProxy(ctx context.Context, owner, id int64) (*service.Proxy, error) {
	row, err := r.client.Proxy.Query().Where(proxy.IDEQ(id), proxy.SupplierUserIDEQ(owner)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, service.ErrSupplierUnavailable
	}
	if err != nil {
		return nil, err
	}
	return proxyEntityToService(row), nil
}
func (r *supplierRepository) CreateProxy(ctx context.Context, owner int64, p *service.Proxy) error {
	p.SupplierUserID = &owner
	pr := newProxyRepositoryWithSQL(r.client, r.sql)
	return pr.Create(ctx, p)
}
func (r *supplierRepository) UpdateProxy(ctx context.Context, owner, id int64, in service.SupplierProxyInput) (*service.Proxy, error) {
	var changed []int64
	err := r.transaction(ctx, func(tr *supplierRepository) error {
		if err := tr.lockProxy(ctx, owner, id); err != nil {
			return err
		}
		sets := []string{"updated_at=NOW()"}
		args := []any{id, owner}
		add := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s=$%d", col, len(args)))
		}
		if in.Name != nil {
			add("name", *in.Name)
		}
		if in.Protocol != nil {
			add("protocol", *in.Protocol)
		}
		if in.Host != nil {
			add("host", *in.Host)
		}
		if in.Port != nil {
			add("port", *in.Port)
		}
		if in.Username != nil {
			add("username", *in.Username)
		}
		if in.Password != nil {
			add("password", *in.Password)
		}
		if in.Status != nil {
			add("status", *in.Status)
		}
		if _, err := tr.sql.ExecContext(ctx, "UPDATE proxies SET "+strings.Join(sets, ",")+" WHERE id=$1 AND supplier_user_id=$2 AND deleted_at IS NULL", args...); err != nil {
			return err
		}
		if _, err := invalidateProxyProbeSnapshots(ctx, tr.sql, id); err != nil {
			return err
		}
		rows, err := tr.sql.QueryContext(ctx, `SELECT id FROM accounts WHERE (proxy_id=$1 OR parent_account_id IN (SELECT id FROM accounts WHERE proxy_id=$1)) AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v int64
			if err = rows.Scan(&v); err != nil {
				_ = rows.Close()
				return err
			}
			changed = append(changed, v)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		return enqueueProxyProbeAccountChanges(ctx, tr.sql, changed)
	})
	if err != nil {
		return nil, err
	}
	r.refresh(ctx, changed)
	return r.GetProxy(ctx, owner, id)
}
func (r *supplierRepository) DeleteProxy(ctx context.Context, owner, id int64) error {
	if _, err := r.GetProxy(ctx, owner, id); err != nil {
		return err
	}
	return r.transaction(ctx, func(tr *supplierRepository) error {
		// Serialize reference writes and count them under the same deletion transaction.
		// Migration 244 revalidates queued writers after this lock is released.
		if _, err := tr.sql.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
			return err
		}
		if _, err := tr.sql.ExecContext(ctx, `SET LOCAL statement_timeout = '10s'`); err != nil {
			return err
		}
		if _, err := tr.sql.ExecContext(ctx, `LOCK TABLE accounts,proxies IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		if err := tr.lockProxy(ctx, owner, id); err != nil {
			return err
		}
		rows, err := tr.sql.QueryContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE (proxy_id=$1 OR proxy_fallback_origin_id=$1) AND deleted_at IS NULL) OR EXISTS(SELECT 1 FROM proxies WHERE backup_proxy_id=$1 AND deleted_at IS NULL)`, id)
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			return service.ErrSupplierUnavailable
		}
		var inUse bool
		err = rows.Scan(&inUse)
		_ = rows.Close()
		if err != nil {
			return err
		}
		if inUse {
			return service.ErrSupplierProxyInUse
		}
		_, err = tr.sql.ExecContext(ctx, `UPDATE proxies SET deleted_at=NOW(),updated_at=NOW() WHERE id=$1 AND supplier_user_id=$2 AND deleted_at IS NULL`, id, owner)
		return err
	})
}
func (r *supplierRepository) Usage(ctx context.Context, owner int64, start, end time.Time, id *int64) (*service.SupplierUsage, error) {
	if id != nil {
		exists, err := r.client.Account.Query().Where(account.IDEQ(*id), account.SupplierUserIDEQ(owner)).Exist(mixins.SkipSoftDelete(ctx))
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, service.ErrSupplierUnavailable
		}
	}
	args := []any{owner, start, end}
	filter := ""
	if id != nil {
		args = append(args, *id)
		filter = " AND a.id=$4"
	}
	// No deleted_at predicate: immutable ownership retains historical usage after administrator deletion.
	rows, err := r.sql.QueryContext(ctx, `SELECT to_char(u.created_at AT TIME ZONE 'UTC','YYYY-MM-DD'),count(*),COALESCE(sum(u.input_tokens),0),COALESCE(sum(u.output_tokens),0),COALESCE(sum(u.cache_creation_tokens),0),COALESCE(sum(u.cache_read_tokens),0) FROM usage_logs u JOIN accounts a ON a.id=u.account_id WHERE a.supplier_user_id=$1 AND u.created_at >= $2 AND u.created_at < $3`+filter+` GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := &service.SupplierUsage{Timezone: "UTC", Daily: []service.SupplierDailyUsage{}}
	for rows.Next() {
		var d service.SupplierDailyUsage
		if err = rows.Scan(&d.Date, &d.Requests, &d.InputTokens, &d.OutputTokens, &d.CacheCreationTokens, &d.CacheReadTokens); err != nil {
			return nil, err
		}
		out.Daily = append(out.Daily, d)
		out.Summary.Requests += d.Requests
		out.Summary.InputTokens += d.InputTokens
		out.Summary.OutputTokens += d.OutputTokens
		out.Summary.CacheCreationTokens += d.CacheCreationTokens
		out.Summary.CacheReadTokens += d.CacheReadTokens
	}
	return out, rows.Err()
}
