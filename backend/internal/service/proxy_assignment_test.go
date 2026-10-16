//go:build unit

package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAssignedSupplierProxyProjection(t *testing.T) {
	owner := int64(42)
	p := &Proxy{ID: 1, Name: "assigned", Protocol: "http", Host: "secret.internal", Port: 8080, Username: "private", Password: "secret", AssignedSupplierUserID: &owner, AssignmentValid: true}
	require.True(t, p.AccessibleToSupplier(owner))
	require.False(t, p.AccessibleToSupplier(43))
	raw, err := json.Marshal(supplierProxyView(p))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got, 6)
	for _, key := range []string{"id", "name", "protocol", "status", "read_only", "expires_at"} {
		require.Contains(t, got, key)
	}
	require.Equal(t, true, got["read_only"])
	p.AssignmentValid = false
	require.False(t, p.AccessibleToSupplier(owner))
}
func TestAssignedFallbackCannotEscapeOwner(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	owner := int64(42)
	other := int64(43)
	backup := int64(2)
	p := Proxy{ID: 1, AssignedSupplierUserID: &owner, AssignmentValid: true, ExpiresAt: &past, FallbackMode: FallbackModeDirect}
	_, change := ResolveProxyFallbackTarget(p, nil, now)
	require.False(t, change)
	p.FallbackMode = FallbackModeProxy
	p.BackupProxyID = &backup
	b := Proxy{ID: 2, AssignedSupplierUserID: &other, AssignmentValid: true, Status: StatusActive}
	_, change = ResolveProxyFallbackTarget(p, map[int64]Proxy{2: b}, now)
	require.False(t, change)
	b.AssignedSupplierUserID = &owner
	target, change := ResolveProxyFallbackTarget(p, map[int64]Proxy{2: b}, now)
	require.True(t, change)
	require.Equal(t, &backup, target)
}

type assignedProxyTestRepo struct {
	SupplierRepository
	p *Proxy
}

func (r *assignedProxyTestRepo) GetProxy(context.Context, int64, int64) (*Proxy, error) {
	if r.p == nil {
		return nil, ErrSupplierUnavailable
	}
	return r.p, nil
}
func TestAssignedSupplierMutationAndOAuthTrust(t *testing.T) {
	owner := int64(42)
	p := &Proxy{ID: 1, Name: "assigned", Protocol: "http", Host: "admin.example.com", Port: 8080, Status: StatusActive, AssignedSupplierUserID: &owner, AssignmentValid: true}
	repo := &assignedProxyTestRepo{p: p}
	s := NewSupplierService(repo, nil)
	_, err := s.UpdateProxy(context.Background(), owner, p.ID, SupplierProxyInput{})
	require.ErrorIs(t, err, ErrSupplierProxyReadOnly)
	require.ErrorIs(t, s.DeleteProxy(context.Background(), owner, p.ID), ErrSupplierProxyReadOnly)
	oauth := NewSupplierOAuthService(s, nil, nil, nil, nil, nil)
	url, err := oauth.proxyURL(context.Background(), owner, &p.ID)
	require.NoError(t, err)
	require.Equal(t, p.URL(), url)
	p.AssignmentValid = false
	_, err = oauth.proxyURL(context.Background(), owner, &p.ID)
	require.ErrorIs(t, err, ErrSupplierUnavailable)
	p.AssignedSupplierUserID = nil
	p.SupplierUserID = &owner
	_, err = oauth.proxyURL(context.Background(), owner, &p.ID)
	require.ErrorIs(t, err, ErrSupplierInvalid)
}
func TestAssignedProxyAdminUpdateRejectsDirect(t *testing.T) {
	owner := int64(42)
	r := &updatingProxyRepoStub{proxyRepoStub: &proxyRepoStub{}, proxy: &Proxy{ID: 1, AssignedSupplierUserID: &owner, AssignmentValid: true, FallbackMode: FallbackModeNone}}
	s := &adminServiceImpl{proxyRepo: r}
	_, err := s.UpdateProxy(context.Background(), 1, &UpdateProxyInput{FallbackMode: FallbackModeDirect})
	require.ErrorIs(t, err, ErrProxyAssignmentInvalid)
	require.Zero(t, r.updateCalls)
}
