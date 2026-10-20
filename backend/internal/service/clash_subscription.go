package service

import (
	"context"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"time"
)

var (
	ErrClashNotFound  = infraerrors.New(404, "CLASH_SUBSCRIPTION_NOT_FOUND", "subscription not found")
	ErrClashForbidden = infraerrors.New(403, "CLASH_SUBSCRIPTION_FORBIDDEN", "subscription unavailable for this user")
	ErrClashConflict  = infraerrors.New(409, "CLASH_SUBSCRIPTION_CONFLICT", "subscription changed; refresh and retry")
	ErrClashNoProxies = infraerrors.New(409, "CLASH_NO_EXPORTABLE_PROXIES", "no exportable proxies")
	ErrClashDuration  = infraerrors.New(400, "CLASH_INVALID_DURATION", "duration must be 30, 90 or 365 days")
	ErrClashDisabled  = infraerrors.New(404, "CLASH_SUBSCRIPTION_DISABLED", "subscription feature disabled")
)

type SubscriptionActor struct {
	UserID int64
	Role   string
}
type ProxySubscription struct {
	ID, UserID, Version  int64
	OwnerRole, TokenHash string
	ExpiresAt            time.Time
	RevokedAt            *time.Time
}
type SubscriptionMetadata struct {
	Exists          bool       `json:"exists"`
	Active          bool       `json:"active"`
	Version         int64      `json:"version"`
	ExpiresAt       *time.Time `json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	ExportableCount int        `json:"exportable_count"`
	SkippedCount    int        `json:"skipped_count"`
}
type SubscriptionGrant struct {
	Metadata        SubscriptionMetadata `json:"metadata"`
	SubscriptionURL string               `json:"subscription_url"`
}

// Callbacks run only in their owner's transaction. They must not perform network I/O.
type ClashSubscriptionTx interface {
	Get(context.Context) (*ProxySubscription, error)
	Proxies(context.Context) ([]Proxy, error)
	Create(context.Context, *ProxySubscription) error
	Reset(context.Context, *ProxySubscription, int64) error
	Revoke(context.Context, int64) error
}
type ClashSubscriptionRepository interface {
	WithLockedOwner(context.Context, SubscriptionActor, func(ClashSubscriptionTx) error) error
	ReadDownloadSnapshot(context.Context, string, func(*ProxySubscription, []Proxy) error) error
}
