package repository

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type clashSubscriptionRepository struct{ db *sql.DB }
type clashSubscriptionTx struct {
	tx    *sql.Tx
	actor service.SubscriptionActor
}

func NewClashSubscriptionRepository(db *sql.DB) service.ClashSubscriptionRepository {
	return &clashSubscriptionRepository{db: db}
}

func (r *clashSubscriptionRepository) WithLockedOwner(ctx context.Context, a service.SubscriptionActor, fn func(service.ClashSubscriptionTx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var role, status string
	var deleted sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT role,status,deleted_at FROM users WHERE id=$1 FOR UPDATE`, a.UserID).Scan(&role, &status, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrClashForbidden
	}
	if err != nil {
		return err
	}
	if deleted.Valid || status != "active" || role != a.Role || (role != "admin" && role != "supplier") {
		return service.ErrClashForbidden
	}
	if err = fn(&clashSubscriptionTx{tx: tx, actor: a}); err != nil {
		return err
	}
	return tx.Commit()
}

func scanClash(row *sql.Row) (*service.ProxySubscription, error) {
	s := new(service.ProxySubscription)
	err := row.Scan(&s.ID, &s.UserID, &s.OwnerRole, &s.TokenHash, &s.Version, &s.ExpiresAt, &s.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

const clashColumns = `id,user_id,owner_role,token_hash,version,expires_at,revoked_at`

func (t *clashSubscriptionTx) Get(ctx context.Context) (*service.ProxySubscription, error) {
	return scanClash(t.tx.QueryRowContext(ctx, `SELECT `+clashColumns+` FROM proxy_subscriptions WHERE user_id=$1`, t.actor.UserID))
}
func (t *clashSubscriptionTx) Proxies(ctx context.Context) ([]service.Proxy, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT id,name,protocol,host,port,COALESCE(username,''),COALESCE(password,'') FROM proxies
 WHERE deleted_at IS NULL AND status='active' AND (expires_at IS NULL OR expires_at>now())
 AND (($2='admin' AND supplier_user_id IS NULL AND assigned_supplier_user_id IS NULL)
 OR ($2='supplier' AND assigned_supplier_user_id=$1)) ORDER BY id`, t.actor.UserID, t.actor.Role)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []service.Proxy{}
	for rows.Next() {
		var p service.Proxy
		if err = rows.Scan(&p.ID, &p.Name, &p.Protocol, &p.Host, &p.Port, &p.Username, &p.Password); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func clashWriteError(err error) error {
	var pe *pq.Error
	if errors.As(err, &pe) && pe.Code == "23505" {
		return service.ErrClashConflict
	}
	return err
}
func (t *clashSubscriptionTx) Create(ctx context.Context, s *service.ProxySubscription) error {
	err := t.tx.QueryRowContext(ctx, `INSERT INTO proxy_subscriptions(user_id,owner_role,token_hash,expires_at) VALUES($1,$2,$3,$4) RETURNING id,version`, t.actor.UserID, t.actor.Role, s.TokenHash, s.ExpiresAt).Scan(&s.ID, &s.Version)
	return clashWriteError(err)
}
func (t *clashSubscriptionTx) Reset(ctx context.Context, s *service.ProxySubscription, expected int64) error {
	err := t.tx.QueryRowContext(ctx, `UPDATE proxy_subscriptions SET token_hash=$2,owner_role=$3,expires_at=$4,revoked_at=NULL,version=version+1,updated_at=now() WHERE user_id=$1 AND version=$5 RETURNING id,version`, t.actor.UserID, s.TokenHash, t.actor.Role, s.ExpiresAt, expected).Scan(&s.ID, &s.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrClashConflict
	}
	return clashWriteError(err)
}
func (t *clashSubscriptionTx) Revoke(ctx context.Context, expected int64) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE proxy_subscriptions SET revoked_at=COALESCE(revoked_at,now()),version=version+1,updated_at=now() WHERE user_id=$1 AND version=$2`, t.actor.UserID, expected)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrClashConflict
	}
	return nil
}
func (r *clashSubscriptionRepository) ReadDownloadSnapshot(ctx context.Context, hash string, fn func(*service.ProxySubscription, []service.Proxy) error) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	s, err := scanClash(tx.QueryRowContext(ctx, `SELECT s.id,s.user_id,s.owner_role,s.token_hash,s.version,s.expires_at,s.revoked_at
 FROM proxy_subscriptions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now()
 AND u.deleted_at IS NULL AND u.status='active' AND u.role=s.owner_role AND u.role IN ('admin','supplier')`, hash))
	if err != nil {
		return err
	}
	if s == nil {
		return service.ErrClashNotFound
	}
	p, err := (&clashSubscriptionTx{tx: tx, actor: service.SubscriptionActor{UserID: s.UserID, Role: s.OwnerRole}}).Proxies(ctx)
	if err != nil {
		return err
	}
	if err = fn(s, p); err != nil {
		return err
	}
	return tx.Commit()
}
