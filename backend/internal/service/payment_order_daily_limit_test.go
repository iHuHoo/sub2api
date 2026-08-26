//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestCreateOrderInTx_SubscriptionDailyLimitUsesStoredUSDAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("daily-limit@example.com").
		SetPasswordHash("hash").
		SetUsername("daily-limit-user").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	req := CreateOrderRequest{UserID: user.ID, PaymentType: payment.TypeAlipay, OrderType: payment.OrderTypeSubscription}
	serviceUser := &User{ID: user.ID, Email: user.Email, Username: user.Username}
	first, err := svc.createOrderInTx(ctx, req, serviceUser, nil, &PaymentConfig{MaxPendingOrders: 3}, 20.86, 149, 0, 149, nil)
	require.NoError(t, err)
	_, err = client.PaymentOrder.UpdateOneID(first.ID).
		SetStatus(OrderStatusCompleted).
		SetPaidAt(time.Now()).
		Save(ctx)
	require.NoError(t, err)

	_, err = svc.createOrderInTx(ctx, req, serviceUser, nil, &PaymentConfig{MaxPendingOrders: 3, DailyLimit: 50}, 20.86, 149, 0, 149, nil)
	require.NoError(t, err)
}
