//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

type assignmentCacheHit struct {
	SchedulerCache
	account *Account
}

func (c *assignmentCacheHit) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return []*Account{c.account}, true, nil
}
func (c *assignmentCacheHit) GetAccount(context.Context, int64) (*Account, error) {
	return c.account, nil
}

func TestAssignmentCacheHitsNeedNoRepository(t *testing.T) {
	id := int64(9)
	cache := &assignmentCacheHit{account: &Account{ID: 1, Status: StatusActive, Schedulable: true, ProxyID: &id}}
	// Embedded nil methods panic on any unexpected repository access.
	s := NewSchedulerSnapshotService(cache, nil, &assignmentNoReadRepo{}, nil, nil)
	accounts, _, err := s.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	a, err := s.GetAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Same(t, cache.account, a)
	require.True(t, a.IsSchedulable())
}

type assignmentNoReadRepo struct{ AccountRepository }

type assignmentOutboxCache struct {
	*bulkEventSnapshotCache
	account *Account
}

func (c *assignmentOutboxCache) SetAccount(_ context.Context, a *Account) error {
	c.account = a
	return nil
}
func TestAssignmentOutboxRefreshesCachedAccount(t *testing.T) {
	owner := int64(9)
	old := &Account{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, SupplierUserID: &owner, Proxy: &Proxy{AssignedSupplierUserID: &owner, AssignmentValid: true, Status: StatusActive}}
	fresh := *old
	fresh.Proxy = &Proxy{AssignedSupplierUserID: &owner, AssignmentValid: false, Status: StatusActive}
	cache := &assignmentOutboxCache{bulkEventSnapshotCache: newBulkEventSnapshotCache(), account: old}
	repo := &assignmentOutboxRepo{bulkEventAccountRepo: newBulkEventAccountRepo(&fresh)}
	s := newBulkEventTestService(cache, repo)
	require.True(t, cache.account.IsSchedulable())
	err := s.handleOutboxEvent(context.Background(), SchedulerOutboxEvent{EventType: SchedulerOutboxEventAccountChanged, AccountID: &fresh.ID}, make(map[batchSeenKey]struct{}))
	require.NoError(t, err)
	require.Same(t, &fresh, cache.account)
	require.False(t, cache.account.IsSchedulable())
	require.NotEmpty(t, cache.capturedBuckets())
}

type assignmentOutboxRepo struct{ *bulkEventAccountRepo }

func (r *assignmentOutboxRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.accounts[0], nil
}
