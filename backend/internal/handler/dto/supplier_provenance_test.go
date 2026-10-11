package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierProvenanceFullAndLite(t *testing.T) {
	id := int64(7)
	full := AccountFromService(&service.Account{ID: 1, SupplierUserID: &id, SupplierName: "Supplier", SupplierPaused: true})
	lite := AccountListItemFromAccount(full)
	for _, value := range []any{full, lite} {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		require.Equal(t, float64(7), got["supplier_user_id"])
		require.Equal(t, "Supplier", got["supplier_name"])
		require.Equal(t, true, got["supplier_paused"])
	}
	platform, err := json.Marshal(AccountFromService(&service.Account{}))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(platform, &got))
	require.Nil(t, got["supplier_user_id"])
}
