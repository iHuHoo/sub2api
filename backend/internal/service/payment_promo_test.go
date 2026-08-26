package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/promocodeusage"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var paymentPromoDBID atomic.Uint64

type paymentPromoLoadBalancer struct {
	selectedAmount float64
}

func (l *paymentPromoLoadBalancer) GetInstanceConfig(context.Context, int64) (map[string]string, error) {
	return nil, nil
}

func (l *paymentPromoLoadBalancer) SelectInstance(_ context.Context, _ string, paymentType payment.PaymentType, _ payment.Strategy, amount float64) (*payment.InstanceSelection, error) {
	l.selectedAmount = amount
	return &payment.InstanceSelection{
		InstanceID:     "promo-test",
		ProviderKey:    payment.TypeEasyPay,
		SupportedTypes: paymentType,
		PaymentMode:    "popup",
		Config: map[string]string{
			"pid": "1", "pkey": "secret", "apiBase": "https://pay.example.com",
			"notifyUrl": "https://api.example.com/notify", "returnUrl": "https://app.example.com/payment/result",
			"paymentMode": "popup",
		},
	}, nil
}

type paymentPromoHarness struct {
	service *service.PaymentService
	client  *dbent.Client
	userID  int64
	planID  int64
	lb      *paymentPromoLoadBalancer
}

func newPaymentPromoHarness(t *testing.T, planPrice, discountRate float64) paymentPromoHarness {
	t.Helper()
	ctx := context.Background()
	id := paymentPromoDBID.Add(1)
	dsn := fmt.Sprintf("file:payment_promo_%d?mode=memory&cache=shared&_fk=1", id)
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("buyer-%d@example.com", id)).
		SetPasswordHash("test").
		SetUsername("buyer").
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName(fmt.Sprintf("subscription-%d", id)).
		SetSubscriptionType(service.SubscriptionTypeSubscription).
		Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("Monthly").
		SetPrice(planPrice).
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)

	settingRepo := repository.NewSettingRepository(client)
	require.NoError(t, settingRepo.Set(ctx, service.SettingPaymentEnabled, "true"))
	require.NoError(t, settingRepo.Set(ctx, service.SettingBalanceRechargeMult, "1"))
	configService := service.NewPaymentConfigService(client, settingRepo, nil)

	promoRepo := repository.NewPromoCodeRepository(client)
	promoService := service.NewPromoService(promoRepo, nil, nil, client, nil)
	_, err = promoService.Create(ctx, &service.CreatePromoCodeInput{
		Code: "SAVE20", Purpose: service.PromoCodePurposeSubscriptionDiscount,
		DiscountRate: &discountRate, MaxUses: 1,
	})
	require.NoError(t, err)

	lb := &paymentPromoLoadBalancer{}
	paymentService := service.NewPaymentService(
		client, payment.NewRegistry(), lb, nil, nil, configService,
		repository.NewUserRepository(client, db), repository.NewGroupRepository(client, db), nil, promoService,
	)
	return paymentPromoHarness{service: paymentService, client: client, userID: user.ID, planID: plan.ID, lb: lb}
}

func (h paymentPromoHarness) order(t *testing.T, orderID int64) *dbent.PaymentOrder {
	t.Helper()
	order, err := h.client.PaymentOrder.Get(context.Background(), orderID)
	require.NoError(t, err)
	return order
}

func (h paymentPromoHarness) usage(t *testing.T) *dbent.PromoCodeUsage {
	t.Helper()
	usage, err := h.client.PromoCodeUsage.Query().
		Where(promocodeusage.UsageTypeEQ(service.PromoCodePurposeSubscriptionDiscount)).
		Only(context.Background())
	require.NoError(t, err)
	return usage
}

func TestCreateSubscriptionOrderReservesPromoAndSnapshotsDiscount(t *testing.T) {
	h := newPaymentPromoHarness(t, 149, 0.8)
	result, err := h.service.CreateOrder(context.Background(), service.CreateOrderRequest{
		UserID: h.userID, OrderType: payment.OrderTypeSubscription, PlanID: h.planID,
		PaymentType: "ldc", PromoCode: " save20 ",
	})
	require.NoError(t, err)

	persisted := h.order(t, result.OrderID)
	require.NotNil(t, persisted.OriginalAmount)
	require.InDelta(t, 149, *persisted.OriginalAmount, 0.000001)
	require.InDelta(t, 119.20, persisted.Amount, 0.000001)
	require.InDelta(t, 29.80, persisted.DiscountAmount, 0.000001)
	require.Equal(t, "SAVE20", *persisted.PromoCode)
	require.Equal(t, service.PromoUsageStatusReserved, h.usage(t).Status)
	require.InDelta(t, 119.20, h.lb.selectedAmount, 0.000001)
}

func TestPreviewSubscriptionPromoUsesPlanPrice(t *testing.T) {
	h := newPaymentPromoHarness(t, 149, 0.8)
	got, err := h.service.PreviewSubscriptionPromo(context.Background(), h.userID, h.planID, "save20")
	require.NoError(t, err)
	require.InDelta(t, 149, got.OriginalAmount, 0.000001)
	require.InDelta(t, 119.20, got.DiscountedAmount, 0.000001)
}

func TestCreateSubscriptionOrderRejectsZeroMinorUnitPromo(t *testing.T) {
	h := newPaymentPromoHarness(t, 0.01, 0.01)
	_, err := h.service.CreateOrder(context.Background(), service.CreateOrderRequest{
		UserID: h.userID, OrderType: payment.OrderTypeSubscription, PlanID: h.planID,
		PaymentType: "ldc", PromoCode: "SAVE20",
	})
	require.Equal(t, "PROMO_CODE_NOT_PAYABLE", infraerrors.Reason(err))
}

func TestBalanceOrderRejectsSubscriptionPromo(t *testing.T) {
	h := newPaymentPromoHarness(t, 149, 0.8)
	_, err := h.service.CreateOrder(context.Background(), service.CreateOrderRequest{
		UserID: h.userID, OrderType: payment.OrderTypeBalance, Amount: 10,
		PaymentType: "ldc", PromoCode: "SAVE20",
	})
	require.Equal(t, "PROMO_CODE_SUBSCRIPTION_ONLY", infraerrors.Reason(err))
	require.False(t, h.client.PaymentOrder.Query().Where(paymentorder.UserIDEQ(h.userID)).ExistX(context.Background()))
}

func TestConcurrentSubscriptionPromoReservationHasOneWinner(t *testing.T) {
	h := newPaymentPromoHarness(t, 149, 0.8)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := h.service.CreateOrder(context.Background(), service.CreateOrderRequest{
				UserID: h.userID, OrderType: payment.OrderTypeSubscription, PlanID: h.planID,
				PaymentType: "ldc", PromoCode: "SAVE20",
			})
			errs <- err
		}()
	}

	var successes, reserved int
	for range 2 {
		err := <-errs
		if err == nil {
			successes++
		} else if infraerrors.Reason(err) == "PROMO_CODE_RESERVED" {
			reserved++
		} else {
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, reserved)
	require.Equal(t, 1, h.client.PromoCodeUsage.Query().
		Where(promocodeusage.StatusEQ(service.PromoUsageStatusReserved)).
		CountX(context.Background()))
}

func TestSubscriptionPromoConvertsAndFeesDiscountedAmount(t *testing.T) {
	h := newPaymentPromoHarness(t, 149, 0.8)
	settings := repository.NewSettingRepository(h.client)
	require.NoError(t, settings.Set(context.Background(), service.SettingSubscriptionUSDToCNYRate, "7"))
	require.NoError(t, settings.Set(context.Background(), service.SettingRechargeFeeRate, "10"))

	result, err := h.service.CreateOrder(context.Background(), service.CreateOrderRequest{
		UserID: h.userID, OrderType: payment.OrderTypeSubscription, PlanID: h.planID,
		PaymentType: "ldc", PromoCode: "SAVE20",
	})
	require.NoError(t, err)
	require.InDelta(t, 917.84, h.lb.selectedAmount, 0.000001)
	require.InDelta(t, 119.20, result.Amount, 0.000001)
	require.InDelta(t, 917.84, result.PayAmount, 0.000001)
}
