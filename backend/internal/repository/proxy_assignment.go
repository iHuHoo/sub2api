package repository

import (
	"context"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func hydrateProxyAssignmentValidity(ctx context.Context, client *dbent.Client, proxies []*service.Proxy) error {
	ids := []int64{}
	for _, p := range proxies {
		if p.AssignedSupplierUserID != nil {
			ids = append(ids, *p.AssignedSupplierUserID)
		} else {
			p.AssignmentValid = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	users, err := client.User.Query().Where(user.IDIn(ids...), user.RoleEQ("supplier"), user.StatusEQ("active"), user.DeletedAtIsNil()).All(ctx)
	if err != nil {
		return err
	}
	valid := map[int64]bool{}
	for _, u := range users {
		valid[u.ID] = true
	}
	for _, p := range proxies {
		if p.AssignedSupplierUserID != nil {
			p.AssignmentValid = valid[*p.AssignedSupplierUserID]
		}
	}
	return nil
}

func (r *proxyRepository) AssignSupplier(ctx context.Context, id int64, expected, target *int64) (*service.Proxy, error) {
	if id <= 0 || target != nil && *target <= 0 || expected != nil && *expected <= 0 {
		return nil, service.ErrProxyAssignmentInvalid
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && err != dbent.ErrTxStarted {
		return nil, err
	}
	client, exec := r.client, r.sql
	if tx != nil {
		client, exec = tx.Client(), tx
		defer tx.Rollback()
	}
	// Reference triggers hold FOR SHARE on this row until commit. Lock it before
	// scanning references so committed bindings are visible and new bindings wait.
	if _, err = exec.ExecContext(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return nil, err
	}
	rows, err := exec.QueryContext(ctx, `SELECT id FROM proxies WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	found := rows.Next()
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	closeErr := rows.Close()
	if closeErr != nil {
		return nil, closeErr
	}
	if !found {
		return nil, service.ErrProxyNotFound
	}
	p, err := client.Proxy.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	equal := func(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	if !equal(p.AssignedSupplierUserID, expected) {
		return proxyEntityToService(p), service.ErrProxyAssignmentConflict
	}
	if p.SupplierUserID != nil {
		return proxyEntityToService(p), service.ErrProxyAssignmentInvalid
	}
	if target != nil {
		rows, err = exec.QueryContext(ctx, `SELECT id FROM users WHERE id=$1 AND role='supplier' AND status='active' AND deleted_at IS NULL FOR SHARE`, *target)
		if err != nil {
			return nil, err
		}
		found = rows.Next()
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		if !found {
			return proxyEntityToService(p), service.ErrProxyAssignmentInvalid
		}
	}
	if !equal(expected, target) {
		rows, err = exec.QueryContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE (proxy_id=$1 OR proxy_fallback_origin_id=$1 OR parent_account_id IN (SELECT id FROM accounts WHERE (proxy_id=$1 OR proxy_fallback_origin_id=$1) AND deleted_at IS NULL)) AND deleted_at IS NULL AND ($2::bigint IS NOT NULL OR $3::bigint IS NULL OR supplier_user_id IS DISTINCT FROM $3)) OR EXISTS(SELECT 1 FROM proxies WHERE backup_proxy_id=$1 AND deleted_at IS NULL)`, id, expected, target)
		if err != nil {
			return nil, err
		}
		var inUse bool
		if rows.Next() {
			err = rows.Scan(&inUse)
		}
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		if inUse {
			return proxyEntityToService(p), service.ErrProxyAssignmentConflict
		}
		if target != nil && p.FallbackMode == service.FallbackModeDirect {
			return proxyEntityToService(p), service.ErrProxyAssignmentInvalid
		}
		if p.BackupProxyID != nil {
			b, err := client.Proxy.Get(ctx, *p.BackupProxyID)
			if err != nil {
				return proxyEntityToService(p), service.ErrProxyAssignmentInvalid
			}
			owner := b.SupplierUserID
			if owner == nil {
				owner = b.AssignedSupplierUserID
			}
			if !equal(owner, target) {
				return proxyEntityToService(p), service.ErrProxyAssignmentInvalid
			}
		}
		if _, err = exec.ExecContext(ctx, `UPDATE proxies SET assigned_supplier_user_id=$2,updated_at=NOW() WHERE id=$1`, id, target); err != nil {
			return nil, normalizeProxyOwnershipError(err)
		}
		changed, err := invalidateProxyProbeSnapshots(ctx, exec, id)
		if err != nil {
			return nil, err
		}
		if err = enqueueProxyProbeAccountChanges(ctx, exec, changed); err != nil {
			return nil, err
		}
	}
	p, err = client.Proxy.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	out := proxyEntityToService(p)
	if err = hydrateProxyAssignmentValidity(ctx, client, []*service.Proxy{out}); err != nil {
		return nil, err
	}
	if tx != nil {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func hydrateProxyAssignmentValues(ctx context.Context, client *dbent.Client, values []service.Proxy) error {
	ps := make([]*service.Proxy, len(values))
	for i := range values {
		ps[i] = &values[i]
	}
	return hydrateProxyAssignmentValidity(ctx, client, ps)
}

func hydrateProxyAssignmentCounts(ctx context.Context, client *dbent.Client, values []service.ProxyWithAccountCount) error {
	ps := make([]*service.Proxy, len(values))
	for i := range values {
		ps[i] = &values[i].Proxy
	}
	return hydrateProxyAssignmentValidity(ctx, client, ps)
}
