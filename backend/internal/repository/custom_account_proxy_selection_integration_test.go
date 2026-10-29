//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type scopedProxySelectionIntegrationAccountRepo struct {
	service.AdminAccountRepository
	nextID int64
}

func (r *scopedProxySelectionIntegrationAccountRepo) Create(_ context.Context, account *service.Account) error {
	r.nextID++
	account.ID = r.nextID
	return nil
}

func (*scopedProxySelectionIntegrationAccountRepo) BindGroups(context.Context, int64, []int64) error {
	return nil
}

func TestScopedAdminAutoProxySelectionExcludesSupplierProxies(t *testing.T) {
	ctx := context.Background()

	// Keep this shared integration database deterministic even if another test
	// left an active proxy behind.
	rows, err := integrationDB.Query(`UPDATE proxies SET status='inactive' WHERE status='active' RETURNING id`)
	require.NoError(t, err)
	var previouslyActive []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		previouslyActive = append(previouslyActive, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	owner := mustCreateUser(t, integrationEntClient, &service.User{Role: service.RoleSupplier})
	assignee := mustCreateUser(t, integrationEntClient, &service.User{Role: service.RoleSupplier})
	var fixtureProxyIDs []int64
	t.Cleanup(func() {
		for _, id := range fixtureProxyIDs {
			_, _ = integrationDB.Exec(`DELETE FROM proxies WHERE id=$1`, id)
		}
		for _, id := range previouslyActive {
			_, _ = integrationDB.Exec(`UPDATE proxies SET status='active' WHERE id=$1`, id)
		}
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE id IN ($1,$2)`, owner.ID, assignee.ID)
	})

	addProxy := func(supplierUserID, assignedSupplierUserID *int64) int64 {
		t.Helper()
		var id int64
		err := integrationDB.QueryRow(`
			INSERT INTO proxies(name,protocol,host,port,status,supplier_user_id,assigned_supplier_user_id)
			VALUES('scoped-admin-selection','http','example.test',8080,'active',$1,$2)
			RETURNING id`, supplierUserID, assignedSupplierUserID).Scan(&id)
		require.NoError(t, err)
		fixtureProxyIDs = append(fixtureProxyIDs, id)
		return id
	}
	addProxy(&owner.ID, nil)
	addProxy(nil, &assignee.ID)

	accountRepo := &scopedProxySelectionIntegrationAccountRepo{}
	proxyRepo := NewProxyRepository(integrationEntClient, integrationDB)
	admin := service.NewAdminService(nil, nil, nil, accountRepo, proxyRepo, nil, nil, nil, nil, nil, nil, nil, nil, integrationEntClient, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	scoped := admin.(service.ScopedAdminCodexAccounts)
	principal := &adminaccess.Principal{Policy: adminaccess.Policy{Rules: []adminaccess.Rule{{
		Permissions:  []string{"accounts.create", "accounts.credentials", "accounts.schedule", "accounts.network"},
		GroupIDs:     []int64{1},
		AccountTypes: []string{service.AccountTypeOAuth},
		Platforms:    []string{service.PlatformOpenAI},
	}}}}
	ctx = adminaccess.WithPrincipal(ctx, principal)
	input := func(mode string) *service.CreateAccountInput {
		return &service.CreateAccountInput{
			Name:        "scoped-admin-selection",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "fixture"},
			GroupIDs:    []int64{1},
			ProxyMode:   mode,
		}
	}

	for _, mode := range []string{"random", "least_used"} {
		t.Run(mode+" returns an empty admin pool", func(t *testing.T) {
			_, err := scoped.ScopedAdminCreateCodex(ctx, input(mode))
			require.Equal(t, "PROXY_POOL_EMPTY", infraerrors.Reason(err))
		})
	}

	adminProxyID := addProxy(nil, nil)
	for _, mode := range []string{"random", "least_used"} {
		t.Run(mode+" selects only an admin proxy", func(t *testing.T) {
			account, err := scoped.ScopedAdminCreateCodex(ctx, input(mode))
			require.NoError(t, err)
			require.NotNil(t, account.ProxyID)
			require.Equal(t, adminProxyID, *account.ProxyID)
		})
	}
}
