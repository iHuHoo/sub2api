package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestClashSubscriptionConfig(t *testing.T) {
	require.NoError(t, (ClashSubscriptionConfig{}).Validate())
	for _, raw := range []string{"", "http://api.example", "https://user:pass@api.example", "https://api.example?q=x", "https://api.example#x"} {
		require.Error(t, (ClashSubscriptionConfig{Enabled: true, PublicBaseURL: raw}).Validate())
	}
	require.NoError(t, (ClashSubscriptionConfig{Enabled: true, PublicBaseURL: "https://api.aoxtoken.com"}).Validate())
}
