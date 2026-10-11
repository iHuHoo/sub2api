package repository

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierOwnershipMappingsAndSchedulerMetadata(t *testing.T) {
	owner := int64(7)
	note := "supplier note"
	account := accountEntityToService(&dbent.Account{ID: 1, SupplierUserID: &owner, SupplierPaused: true, SupplierNotes: &note, Status: service.StatusActive, Schedulable: true})
	require.Equal(t, &owner, account.SupplierUserID)
	require.Equal(t, &note, account.SupplierNotes)
	require.True(t, account.SupplierPaused)
	require.False(t, account.IsSchedulable())
	metadata := buildSchedulerMetadataAccount(*account)
	require.Equal(t, &owner, metadata.SupplierUserID)
	require.True(t, metadata.SupplierPaused)
	proxy := proxyEntityToService(&dbent.Proxy{ID: 1, SupplierUserID: &owner})
	require.Equal(t, &owner, proxy.SupplierUserID)
	user := userEntityToService(&dbent.User{ID: 7, AuthVersion: 3})
	require.Equal(t, int64(3), user.AuthVersion)
}
