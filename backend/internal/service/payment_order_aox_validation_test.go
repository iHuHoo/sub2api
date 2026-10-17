//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// The upstream order whitelist must coexist with Aox subscription-only promos.
func TestValidateOrderInputAoxPromoAndOrderType(t *testing.T) {
	svc := &PaymentService{}
	for _, tc := range []struct {
		name, orderType, promoCode, wantReason string
	}{
		{"unknown type", "unknown", "", "INVALID_ORDER_TYPE"},
		{"balance promo", payment.OrderTypeBalance, " SAVE20 ", infraerrors.Reason(ErrPromoCodeSubscriptionOnly)},
		{"balance without promo", payment.OrderTypeBalance, " ", ""},
		{"subscription promo reaches plan validation", payment.OrderTypeSubscription, "SAVE20", "INVALID_INPUT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.validateOrderInput(context.Background(), CreateOrderRequest{
				OrderType: tc.orderType, PromoCode: tc.promoCode, Amount: 10,
			}, &PaymentConfig{})
			if tc.wantReason == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, tc.wantReason, infraerrors.Reason(err))
			}
		})
	}
}
