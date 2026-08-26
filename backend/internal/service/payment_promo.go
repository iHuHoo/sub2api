package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/promocodeusage"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

var (
	ErrPromoCodeSubscriptionOnly = infraerrors.BadRequest("PROMO_CODE_SUBSCRIPTION_ONLY", "promo code can only be used for subscription orders")
	ErrPromoCodeUnavailable      = infraerrors.ServiceUnavailable("PROMO_CODE_UNAVAILABLE", "promo code service is unavailable")
)

type paymentProviderNotStartedError struct{ err error }

func (e *paymentProviderNotStartedError) Error() string { return e.err.Error() }
func (e *paymentProviderNotStartedError) Unwrap() error { return e.err }

func (s *PaymentService) PreviewSubscriptionPromo(ctx context.Context, userID, planID int64, code string) (*SubscriptionPromoPreview, error) {
	if userID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "authenticated user is required")
	}
	if s.promoService == nil {
		return nil, ErrPromoCodeUnavailable
	}
	plan, err := s.validateSubOrder(ctx, CreateOrderRequest{OrderType: payment.OrderTypeSubscription, PlanID: planID})
	if err != nil {
		return nil, err
	}
	return s.promoService.ValidateSubscriptionPromo(ctx, code, plan.Price)
}

func (s *PaymentService) lockSubscriptionPromo(ctx context.Context, tx *dbent.Tx, req CreateOrderRequest, planPrice, discountedAmount float64) (*SubscriptionPromoPreview, *PromoCode, error) {
	if strings.TrimSpace(req.PromoCode) == "" {
		return nil, nil, nil
	}
	if s.promoService == nil {
		return nil, nil, ErrPromoCodeUnavailable
	}

	preview, promoCode, err := s.promoService.validateSubscriptionPromoForUpdate(dbent.NewTxContext(ctx, tx), req.PromoCode, planPrice)
	if err != nil {
		return nil, nil, err
	}
	want := decimal.NewFromFloat(discountedAmount).Round(2)
	got := decimal.NewFromFloat(preview.DiscountedAmount).Round(2)
	if !want.Equal(got) {
		return nil, nil, ErrPromoCodeInvalid
	}
	return preview, promoCode, nil
}

func (s *PaymentService) reserveSubscriptionPromo(ctx context.Context, orderID, userID int64, promoCode *PromoCode, preview *SubscriptionPromoPreview) error {
	if promoCode == nil || preview == nil {
		return nil
	}
	now := time.Now().UTC()
	usage := &PromoCodeUsage{
		PromoCodeID:    promoCode.ID,
		UserID:         userID,
		PaymentOrderID: &orderID,
		UsageType:      PromoCodePurposeSubscriptionDiscount,
		Status:         PromoUsageStatusReserved,
		DiscountAmount: preview.DiscountAmount,
		UsedAt:         now,
		ReservedAt:     &now,
	}
	if err := s.promoService.promoRepo.CreateUsage(ctx, usage); err != nil {
		if dbent.IsConstraintError(err) {
			return ErrPromoCodeReserved
		}
		return fmt.Errorf("reserve subscription promo: %w", err)
	}
	return nil
}

func (s *PaymentService) consumeSubscriptionPromoForPaidOrder(ctx context.Context, o *dbent.PaymentOrder, tradeNo string, paid float64, providerKey string) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin promo payment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	orderQuery := tx.PaymentOrder.Query().Where(paymentorder.IDEQ(o.ID))
	if tx.Client().Driver().Dialect() != dialect.SQLite {
		orderQuery = orderQuery.ForUpdate()
	}
	current, err := orderQuery.Only(ctx)
	if err != nil {
		return fmt.Errorf("lock promo payment order: %w", err)
	}
	usageQuery := tx.PromoCodeUsage.Query().Where(
		promocodeusage.PaymentOrderIDEQ(o.ID),
		promocodeusage.UsageTypeEQ(PromoCodePurposeSubscriptionDiscount),
	)
	if tx.Client().Driver().Dialect() != dialect.SQLite {
		usageQuery = usageQuery.ForUpdate()
	}
	usage, err := usageQuery.Only(ctx)
	if err != nil {
		return fmt.Errorf("lock promo reservation: %w", err)
	}

	now := time.Now().UTC()
	graceStart := now.Add(-paymentGraceMinutes * time.Minute)
	if usage.Status == PromoUsageStatusReleased ||
		((current.Status == OrderStatusCancelled || current.Status == OrderStatusExpired || current.Status == OrderStatusFailed) && !current.UpdatedAt.After(graceStart)) {
		if usage.Status == PromoUsageStatusReserved {
			if _, err := tx.PromoCodeUsage.UpdateOneID(usage.ID).
				SetStatus(PromoUsageStatusReleased).
				SetReleasedAt(now).
				Save(ctx); err != nil {
				return fmt.Errorf("release promo after late payment: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit late promo payment rejection: %w", err)
		}
		s.writeAuditLog(ctx, o.ID, "PAYMENT_AFTER_PROMO_RELEASE", providerKey, map[string]any{
			"status": current.Status, "tradeNo": tradeNo, "paidAmount": paid,
		})
		return nil
	}

	if usage.Status == PromoUsageStatusConsumed {
		switch current.Status {
		case OrderStatusPaid, OrderStatusRecharging, OrderStatusCompleted, OrderStatusRefunded, OrderStatusFailed:
			_ = tx.Rollback()
			return s.alreadyProcessed(ctx, current)
		}
	}
	eligible := current.Status == OrderStatusPending ||
		current.Status == OrderStatusCancelled ||
		current.Status == OrderStatusExpired ||
		current.Status == OrderStatusFailed
	if !eligible {
		_ = tx.Rollback()
		return s.alreadyProcessed(ctx, current)
	}

	if usage.Status == PromoUsageStatusReserved {
		updated, err := tx.PromoCodeUsage.Update().
			Where(promocodeusage.IDEQ(usage.ID), promocodeusage.StatusEQ(PromoUsageStatusReserved)).
			SetStatus(PromoUsageStatusConsumed).
			SetConsumedAt(now).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("consume promo reservation: %w", err)
		}
		if updated != 1 {
			return ErrPromoCodeReserved
		}
		if _, err := tx.PromoCode.UpdateOneID(usage.PromoCodeID).AddUsedCount(1).Save(ctx); err != nil {
			return fmt.Errorf("increment consumed promo count: %w", err)
		}
	} else if usage.Status != PromoUsageStatusConsumed {
		return fmt.Errorf("invalid promo usage status: %s", usage.Status)
	}

	previousStatus := current.Status
	if _, err := tx.PaymentOrder.UpdateOneID(current.ID).
		SetStatus(OrderStatusPaid).
		SetPayAmount(paid).
		SetPaymentTradeNo(tradeNo).
		SetPaidAt(now).
		ClearFailedAt().
		ClearFailedReason().
		Save(ctx); err != nil {
		return fmt.Errorf("update discounted order to PAID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit discounted order payment: %w", err)
	}
	if previousStatus != OrderStatusPending {
		s.writeAuditLog(ctx, o.ID, "ORDER_RECOVERED", providerKey, map[string]any{
			"previous_status": previousStatus, "tradeNo": tradeNo, "paidAmount": paid,
		})
	}
	s.writeAuditLog(ctx, o.ID, "ORDER_PAID", providerKey, map[string]any{"tradeNo": tradeNo, "paidAmount": paid})
	return s.executeFulfillment(ctx, o.ID)
}

func (s *PaymentService) releaseSubscriptionPromoForOrder(ctx context.Context, orderID int64, now time.Time) (int, error) {
	count, err := s.entClient.PromoCodeUsage.Update().Where(
		promocodeusage.PaymentOrderIDEQ(orderID),
		promocodeusage.UsageTypeEQ(PromoCodePurposeSubscriptionDiscount),
		promocodeusage.StatusEQ(PromoUsageStatusReserved),
	).
		SetStatus(PromoUsageStatusReleased).
		SetReleasedAt(now.UTC()).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("release subscription promo: %w", err)
	}
	return count, nil
}

func (s *PaymentService) ReleaseFinalSubscriptionPromoReservations(ctx context.Context, now time.Time) (int, error) {
	orderIDs, err := s.entClient.PaymentOrder.Query().Where(
		paymentorder.PromoCodeIDNotNil(),
		paymentorder.StatusIn(OrderStatusCancelled, OrderStatusExpired, OrderStatusFailed),
		paymentorder.UpdatedAtLTE(now.UTC().Add(-paymentGraceMinutes*time.Minute)),
	).IDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("query final promo orders: %w", err)
	}
	if len(orderIDs) == 0 {
		return 0, nil
	}
	count, err := s.entClient.PromoCodeUsage.Update().Where(
		promocodeusage.PaymentOrderIDIn(orderIDs...),
		promocodeusage.UsageTypeEQ(PromoCodePurposeSubscriptionDiscount),
		promocodeusage.StatusEQ(PromoUsageStatusReserved),
	).
		SetStatus(PromoUsageStatusReleased).
		SetReleasedAt(now.UTC()).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("release final promo reservations: %w", err)
	}
	return count, nil
}
