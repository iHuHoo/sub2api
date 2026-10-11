package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierRenameChangesFullAndLiteListETag(t *testing.T) {
	owner := int64(7)
	full := dto.AccountFromService(&service.Account{ID: 1, SupplierUserID: &owner, SupplierName: "before"})
	lite := dto.AccountListItemFromAccount(full)
	beforeFull := buildAccountsListETag([]*dto.Account{full}, 1, 1, 20, "", "", "", "", false)
	beforeLite := buildAccountsListETag([]*dto.AccountListItem{lite}, 1, 1, 20, "", "", "", "", true)
	full.SupplierName = "after"
	lite.SupplierName = "after"
	require.NotEqual(t, beforeFull, buildAccountsListETag([]*dto.Account{full}, 1, 1, 20, "", "", "", "", false))
	require.NotEqual(t, beforeLite, buildAccountsListETag([]*dto.AccountListItem{lite}, 1, 1, 20, "", "", "", "", true))
}
