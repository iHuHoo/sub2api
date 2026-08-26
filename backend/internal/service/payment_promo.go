package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

var (
	ErrPromoCodeSubscriptionOnly = infraerrors.BadRequest("PROMO_CODE_SUBSCRIPTION_ONLY", "promo code can only be used for subscription orders")
	ErrPromoCodeUnavailable      = infraerrors.ServiceUnavailable("PROMO_CODE_UNAVAILABLE", "promo code service is unavailable")
)

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
