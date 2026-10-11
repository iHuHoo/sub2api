//go:build unit

package admin

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSupplierCodexParserUsesOnlyAuthenticationFields(t *testing.T) {
	rows, err := ParseSupplierCodexSessions(`{"name":"imported","tokens":{"access_token":"at","refresh_token":"rt"},"group_ids":[1],"priority":100,"extra":{"secret":"hidden"}}`, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "at", rows[0].Credentials["access_token"])
	require.Equal(t, "rt", rows[0].Credentials["refresh_token"])
	require.NotContains(t, rows[0].Credentials, "extra")
	require.Nil(t, rows[0].ProxyID)
	_, err = ParseSupplierCodexSessions(`{"auth_mode":"agent_identity","agent_identity":{}}`, nil)
	require.Error(t, err)
	_, err = ParseSupplierCodexSessions(`[]`, nil)
	require.Error(t, err)
}
