package service

import (
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
		{"unhydrated proxy", nil, true},
		{"ordinary proxy", &Proxy{Status: StatusDisabled}, true},
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
