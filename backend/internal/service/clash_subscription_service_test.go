package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

type clashTestStore struct {
	record  *ProxySubscription
	proxies []Proxy
}

func (f *clashTestStore) WithLockedOwner(ctx context.Context, a SubscriptionActor, fn func(ClashSubscriptionTx) error) error {
	return fn(f)
}
func (f *clashTestStore) ReadDownloadSnapshot(ctx context.Context, hash string, fn func(*ProxySubscription, []Proxy) error) error {
	if f.record == nil || hash != f.record.TokenHash || f.record.RevokedAt != nil {
		return ErrClashNotFound
	}
	return fn(f.record, f.proxies)
}
func (f *clashTestStore) Get(context.Context) (*ProxySubscription, error) { return f.record, nil }
func (f *clashTestStore) Proxies(context.Context) ([]Proxy, error)        { return f.proxies, nil }
func (f *clashTestStore) Create(_ context.Context, s *ProxySubscription) error {
	s.Version = 1
	s.ID = 1
	f.record = s
	return nil
}
func (f *clashTestStore) Reset(_ context.Context, s *ProxySubscription, v int64) error {
	s.Version = v + 1
	s.ID = 1
	f.record = s
	return nil
}
func (f *clashTestStore) Revoke(context.Context, int64) error { return nil }
func TestClashSubscriptionLifecycle(t *testing.T) {
	ctx := context.Background()
	a := SubscriptionActor{UserID: 1, Role: RoleAdmin}
	f := &clashTestStore{proxies: []Proxy{{ID: 1, Protocol: "http", Host: "example.test", Port: 80}}}
	s := NewClashSubscriptionService(f, &config.Config{ClashSubscription: config.ClashSubscriptionConfig{Enabled: true, PublicBaseURL: "https://api.example"}})
	_, err := s.Create(ctx, a, 7)
	require.ErrorIs(t, err, ErrClashDuration)
	g, err := s.Create(ctx, a, 0)
	require.NoError(t, err)
	require.Contains(t, g.SubscriptionURL, "https://api.example/api/v1/clash/subscription?token=")
	require.NotContains(t, g.SubscriptionURL, f.record.TokenHash)
	require.Len(t, f.record.TokenHash, 64)
	_, err = s.Create(ctx, a, 90)
	require.ErrorIs(t, err, ErrClashConflict)
	_, err = s.Reset(ctx, a, 2, 90)
	require.ErrorIs(t, err, ErrClashConflict)
	old := *f.record
	f.proxies = nil
	_, err = s.Reset(ctx, a, 1, 90)
	require.ErrorIs(t, err, ErrClashNoProxies)
	require.Equal(t, old, *f.record)
	m, err := s.GetMetadata(ctx, a)
	require.NoError(t, err)
	require.True(t, m.Active)
	require.Zero(t, m.ExportableCount)
}
