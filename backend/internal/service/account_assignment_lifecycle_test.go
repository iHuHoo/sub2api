package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAccountAssignedProxyAvailability(t *testing.T) {
	id := int64(9)
	past := time.Now().Add(-time.Hour)
	for _, tt := range []struct {
		name string
		p    *Proxy
		want bool
	}{
		{"invalid owner", &Proxy{AssignedSupplierUserID: &id, Status: StatusActive}, false},
		{"valid owner", &Proxy{AssignedSupplierUserID: &id, AssignmentValid: true, Status: StatusActive}, true},
		{"expired owner", &Proxy{AssignedSupplierUserID: &id, AssignmentValid: true, Status: StatusActive, ExpiresAt: &past}, false},
		{"disabled proxy", &Proxy{AssignedSupplierUserID: &id, AssignmentValid: true, Status: StatusDisabled}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := &Account{Status: StatusActive, Schedulable: true, SupplierUserID: &id, ProxyID: &id, Proxy: tt.p}
			if a.IsSchedulable() != tt.want {
				t.Errorf("schedulable=%v want %v", a.IsSchedulable(), tt.want)
			}
			if a.IsCredentialUsableForShadow() != tt.want {
				t.Errorf("shadow=%v want %v", a.IsCredentialUsableForShadow(), tt.want)
			}
			if !a.Schedulable || a.SupplierPaused {
				t.Fatal("preferences mutated")
			}
		})
	}
}

type assignmentRefreshRepo struct {
	AccountRepository
	calls int
	err   error
	valid bool
}

func (r *assignmentRefreshRepo) RefreshAccountProxies(_ context.Context, accounts []*Account) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	id := int64(9)
	for _, a := range accounts {
		a.SupplierUserID = &id
		a.Proxy = &Proxy{AssignedSupplierUserID: &id, AssignmentValid: r.valid, Status: StatusActive}
	}
	return nil
}
func TestCachedProxyLifecycleRefresh(t *testing.T) {
	id := int64(1)
	r := &assignmentRefreshRepo{}
	s := &SchedulerSnapshotService{accountRepo: r}
	// A legacy payload without assignment metadata must still be checked live.
	accounts := []*Account{{Status: StatusActive, Schedulable: true, ProxyID: &id, Proxy: &Proxy{Status: StatusActive}}, {Status: StatusActive, Schedulable: true}}
	if err := s.refreshCachedAccountProxies(context.Background(), accounts); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 || accounts[0].IsSchedulable() || !accounts[1].IsSchedulable() {
		t.Fatal("stale owner accepted or direct account rejected")
	}
	r.valid = true
	if err := s.refreshCachedAccountProxies(context.Background(), accounts); err != nil || !accounts[0].IsSchedulable() {
		t.Fatal("supplier restoration failed")
	}
	r.err = errors.New("database unavailable")
	if s.refreshCachedAccountProxies(context.Background(), accounts) == nil {
		t.Fatal("database error accepted")
	}
	s.accountRepo = nil
	if s.refreshCachedAccountProxies(context.Background(), accounts) == nil {
		t.Fatal("missing validation capability accepted")
	}
	if err := s.refreshCachedAccountProxies(context.Background(), accounts[1:]); err == nil {
		t.Fatal("direct account must also require validation")
	}
}

func TestAccountAssignedProxyRejectsForeignOwner(t *testing.T) {
	owner, foreign := int64(9), int64(10)
	a := &Account{SupplierUserID: &foreign, ProxyID: &owner, Proxy: &Proxy{AssignedSupplierUserID: &owner, AssignmentValid: true, Status: StatusActive}}
	if a.proxyAssignmentUsable() {
		t.Fatal("foreign assigned proxy accepted")
	}
	a.SupplierUserID = nil
	if a.proxyAssignmentUsable() {
		t.Fatal("admin account accepted assigned proxy")
	}
}

// bindingFixtureRepo models the authoritative account projection independently of cached objects.
type bindingFixtureRepo struct {
	AccountRepository
	bindings map[int64]Account
}

func (r bindingFixtureRepo) RefreshAccountProxies(_ context.Context, accounts []*Account) error {
	for _, a := range accounts {
		b, ok := r.bindings[a.ID]
		a.ProxyBindingMissing = !ok
		a.ProxyID, a.Proxy, a.SupplierUserID = b.ProxyID, b.Proxy, b.SupplierUserID
	}
	return nil
}
func bindingFixtures(accounts ...*Account) bindingFixtureRepo {
	r := bindingFixtureRepo{bindings: map[int64]Account{}}
	for _, a := range accounts {
		if a != nil {
			r.bindings[a.ID] = *a
		}
	}
	return r
}
