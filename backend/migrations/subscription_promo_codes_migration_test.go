package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionPromoCodeMigrationPreservesRegistrationCodes(t *testing.T) {
	content, err := FS.ReadFile("231_subscription_promo_codes.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS purpose VARCHAR(32) NOT NULL DEFAULT 'registration_bonus'")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS discount_rate DECIMAL(10,4)")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS usage_type VARCHAR(32) NOT NULL DEFAULT 'registration_bonus'")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'consumed'")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS payment_order_id BIGINT")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS promo_code_id BIGINT")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS original_amount DECIMAL(20,2)")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS discount_amount DECIMAL(20,2) NOT NULL DEFAULT 0")
	require.Contains(t, sql, "WHERE usage_type = 'registration_bonus'")
	require.Contains(t, sql, "WHERE usage_type = 'subscription_discount' AND status = 'reserved'")
	require.Contains(t, sql, "WHERE usage_type = 'subscription_discount' AND status = 'consumed'")
	require.Contains(t, sql, "UPDATE promo_code_usages SET consumed_at = used_at")
}
