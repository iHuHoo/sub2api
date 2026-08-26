package integration_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/promocode"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func createReservedPromoOrder(t *testing.T) (paymentPromoHarness, int64) {
	t.Helper()
	h := newPaymentPromoHarness(t, 149, 0.8)
	result, err := h.service.CreateOrder(context.Background(), service.CreateOrderRequest{
		UserID: h.userID, OrderType: payment.OrderTypeSubscription, PlanID: h.planID,
		PaymentType: "ldc", PromoCode: "SAVE20",
	})
	require.NoError(t, err)
	return h, result.OrderID
}

func notifyPromoOrderPaid(h paymentPromoHarness, orderID int64) error {
	ctx := context.Background()
	order, err := h.client.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return err
	}
	return h.service.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		OrderID: order.OutTradeNo, TradeNo: "trade-paid", Amount: order.PayAmount,
		Status: payment.NotificationStatusSuccess, Metadata: map[string]string{"pid": "1"},
	}, payment.TypeEasyPay)
}

func TestPaidDiscountedOrderConsumesCodeExactlyOnce(t *testing.T) {
	h, orderID := createReservedPromoOrder(t)
	require.NoError(t, notifyPromoOrderPaid(h, orderID))
	require.NoError(t, notifyPromoOrderPaid(h, orderID))

	require.Equal(t, service.PromoUsageStatusConsumed, h.usage(t).Status)
	promo, err := h.client.PromoCode.Query().Where(promocode.CodeEQ("SAVE20")).Only(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, promo.UsedCount)
	require.Equal(t, 1, h.client.UserSubscription.Query().CountX(context.Background()))
}

func TestReleasedPromoRejectsLatePayment(t *testing.T) {
	h, orderID := createReservedPromoOrder(t)
	transitionedAt := time.Now().Add(-6 * time.Minute)
	_, err := h.client.PaymentOrder.UpdateOneID(orderID).
		SetStatus(service.OrderStatusExpired).
		SetUpdatedAt(transitionedAt).
		Save(context.Background())
	require.NoError(t, err)
	_, err = h.client.PromoCodeUsage.UpdateOneID(h.usage(t).ID).
		SetStatus(service.PromoUsageStatusReleased).
		SetReleasedAt(transitionedAt.Add(5 * time.Minute)).
		Save(context.Background())
	require.NoError(t, err)

	require.NoError(t, notifyPromoOrderPaid(h, orderID))
	require.Equal(t, service.OrderStatusExpired, h.order(t, orderID).Status)
	require.Zero(t, h.client.UserSubscription.Query().CountX(context.Background()))
	require.True(t, h.client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)),
		paymentauditlog.ActionEQ("PAYMENT_AFTER_PROMO_RELEASE"),
	).ExistX(context.Background()))
}

func TestSubscriptionPromoReleaseWaitsForGrace(t *testing.T) {
	h, orderID := createReservedPromoOrder(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	transitionedAt := now.Add(-4*time.Minute - 59*time.Second)
	_, err := h.client.PaymentOrder.UpdateOneID(orderID).
		SetStatus(service.OrderStatusCancelled).
		SetUpdatedAt(transitionedAt).
		Save(context.Background())
	require.NoError(t, err)

	count, err := h.service.ReleaseFinalSubscriptionPromoReservations(context.Background(), now)
	require.NoError(t, err)
	require.Zero(t, count)

	count, err = h.service.ReleaseFinalSubscriptionPromoReservations(context.Background(), now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, service.PromoUsageStatusReleased, h.usage(t).Status)

	count, err = h.service.ReleaseFinalSubscriptionPromoReservations(context.Background(), now.Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestSubscriptionPromoPaymentInsideGraceConsumes(t *testing.T) {
	h, orderID := createReservedPromoOrder(t)
	_, err := h.client.PaymentOrder.UpdateOneID(orderID).
		SetStatus(service.OrderStatusExpired).
		SetUpdatedAt(time.Now().Add(-4*time.Minute - 59*time.Second)).
		Save(context.Background())
	require.NoError(t, err)

	require.NoError(t, notifyPromoOrderPaid(h, orderID))
	require.Equal(t, service.PromoUsageStatusConsumed, h.usage(t).Status)
}

func TestSubscriptionPromoPreProviderFailureReleasesImmediately(t *testing.T) {
	h := newPaymentPromoHarness(t, 149, 0.8)
	h.lb.invalidProviderConfig = true
	_, err := h.service.CreateOrder(context.Background(), service.CreateOrderRequest{
		UserID: h.userID, OrderType: payment.OrderTypeSubscription, PlanID: h.planID,
		PaymentType: "ldc", PromoCode: "SAVE20",
	})
	require.Error(t, err)
	require.Equal(t, service.PromoUsageStatusReleased, h.usage(t).Status)

	h.lb.invalidProviderConfig = false
	_, err = h.service.CreateOrder(context.Background(), service.CreateOrderRequest{
		UserID: h.userID, OrderType: payment.OrderTypeSubscription, PlanID: h.planID,
		PaymentType: "ldc", PromoCode: "SAVE20",
	})
	require.NoError(t, err)
}
