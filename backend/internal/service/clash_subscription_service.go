package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"strings"
	"time"
)

type ClashSubscriptionService struct {
	repo ClashSubscriptionRepository
	cfg  config.ClashSubscriptionConfig
}

func NewClashSubscriptionService(repo ClashSubscriptionRepository, cfg *config.Config) *ClashSubscriptionService {
	return &ClashSubscriptionService{repo: repo, cfg: cfg.ClashSubscription}
}
func (s *ClashSubscriptionService) Enabled() bool         { return s.cfg.Enabled }
func (s *ClashSubscriptionService) PublicBaseURL() string { return s.cfg.PublicBaseURL }
func ClashTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func clashMetadata(record *ProxySubscription, set ClashExportSet) SubscriptionMetadata {
	m := SubscriptionMetadata{ExportableCount: len(set.Proxies), SkippedCount: set.SkippedCount, SkippedReasons: set.SkippedReasons}
	if record != nil {
		m.Exists = true
		m.Version = record.Version
		m.ExpiresAt = &record.ExpiresAt
		m.RevokedAt = record.RevokedAt
		m.Active = record.RevokedAt == nil && record.ExpiresAt.After(time.Now())
	}
	return m
}
func (s *ClashSubscriptionService) GetMetadata(ctx context.Context, a SubscriptionActor) (SubscriptionMetadata, error) {
	var m SubscriptionMetadata
	if !s.Enabled() {
		return m, ErrClashDisabled
	}
	err := s.repo.ReadMetadataSnapshot(ctx, a, func(tx ClashSubscriptionTx) error {
		r, e := tx.Get(ctx)
		if e != nil {
			return e
		}
		p, e := tx.Proxies(ctx)
		if e != nil {
			return e
		}
		m = clashMetadata(r, FilterClashProxies(p))
		return nil
	})
	return m, err
}
func (s *ClashSubscriptionService) Create(ctx context.Context, a SubscriptionActor, days int) (SubscriptionGrant, error) {
	return s.grant(ctx, a, 0, days, false)
}
func (s *ClashSubscriptionService) Reset(ctx context.Context, a SubscriptionActor, version int64, days int) (SubscriptionGrant, error) {
	return s.grant(ctx, a, version, days, true)
}
func (s *ClashSubscriptionService) grant(ctx context.Context, a SubscriptionActor, version int64, days int, reset bool) (SubscriptionGrant, error) {
	var g SubscriptionGrant
	if !s.Enabled() {
		return g, ErrClashDisabled
	}
	if days == 0 {
		days = 90
	}
	if days != 30 && days != 90 && days != 365 {
		return g, ErrClashDuration
	}
	err := s.repo.WithLockedOwner(ctx, a, func(tx ClashSubscriptionTx) error {
		old, e := tx.Get(ctx)
		if e != nil {
			return e
		}
		if !reset && old != nil || reset && (old == nil || old.Version != version || version < 1) {
			return ErrClashConflict
		}
		p, e := tx.Proxies(ctx)
		if e != nil {
			return e
		}
		set := FilterClashProxies(p)
		if len(set.Proxies) == 0 {
			return ErrClashNoProxies
		}
		var raw [32]byte
		if _, e = rand.Read(raw[:]); e != nil {
			return e
		}
		token := base64.RawURLEncoding.EncodeToString(raw[:])
		r := &ProxySubscription{UserID: a.UserID, OwnerRole: a.Role, TokenHash: ClashTokenHash(token), ExpiresAt: time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)}
		if reset {
			e = tx.Reset(ctx, r, version)
		} else {
			e = tx.Create(ctx, r)
		}
		if e != nil {
			return e
		}
		g = SubscriptionGrant{Metadata: clashMetadata(r, set), SubscriptionURL: strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/api/v1/clash/subscription?token=" + token}
		return nil
	})
	if err != nil {
		return SubscriptionGrant{}, err
	}
	return g, nil
}
func (s *ClashSubscriptionService) Revoke(ctx context.Context, a SubscriptionActor, version int64) error {
	if !s.Enabled() {
		return ErrClashDisabled
	}
	if version < 1 {
		return ErrClashConflict
	}
	return s.repo.WithLockedOwner(ctx, a, func(tx ClashSubscriptionTx) error { return tx.Revoke(ctx, version) })
}

// Snapshot returns credentials only to the download handler, never to metadata or logs.
func (s *ClashSubscriptionService) Snapshot(ctx context.Context, token string) (int64, ClashExportSet, error) {
	var id int64
	var set ClashExportSet
	if !s.Enabled() {
		return 0, set, ErrClashDisabled
	}
	raw, e := base64.RawURLEncoding.DecodeString(token)
	if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return 0, set, ErrClashNotFound
	}
	e = s.repo.ReadDownloadSnapshot(ctx, ClashTokenHash(token), func(r *ProxySubscription, p []Proxy) error { id = r.ID; set = FilterClashProxies(p); return nil })
	return id, set, e
}
