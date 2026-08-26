package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var promoServiceDBID atomic.Uint64

type promoServiceTestHarness struct {
	service *service.PromoService
	client  *dbent.Client
	promoID int64
	userID  int64
}

func newPromoServiceTestHarness(t *testing.T, promo service.PromoCode) promoServiceTestHarness {
	t.Helper()

	dsn := fmt.Sprintf("file:promo_service_%d?mode=memory&cache=shared&_fk=1", promoServiceDBID.Add(1))
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	repo := repository.NewPromoCodeRepository(client)
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("promo-%d@example.com", promoServiceDBID.Load())).
		SetPasswordHash("test").
		Save(context.Background())
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), &promo))

	return promoServiceTestHarness{
		service: service.NewPromoService(repo, nil, nil, client, nil),
		client:  client,
		promoID: promo.ID,
		userID:  user.ID,
	}
}

func ptr[T any](value T) *T { return &value }

func TestValidateSubscriptionPromoCalculatesPayablePercentage(t *testing.T) {
	h := newPromoServiceTestHarness(t, service.PromoCode{
		Code: "SAVE20", Purpose: service.PromoCodePurposeSubscriptionDiscount,
		DiscountRate: ptr(0.8), MaxUses: 1, Status: service.PromoCodeStatusActive,
	})

	got, err := h.service.ValidateSubscriptionPromo(context.Background(), " save20 ", 149)
	require.NoError(t, err)
	require.Equal(t, "SAVE20", got.Code)
	require.InDelta(t, 29.80, got.DiscountAmount, 0.000001)
	require.InDelta(t, 119.20, got.DiscountedAmount, 0.000001)
}

func TestRegistrationValidatorRejectsSubscriptionPromo(t *testing.T) {
	h := newPromoServiceTestHarness(t, service.PromoCode{
		Code: "SAVE20", Purpose: service.PromoCodePurposeSubscriptionDiscount,
		DiscountRate: ptr(0.8), MaxUses: 1, Status: service.PromoCodeStatusActive,
	})

	_, err := h.service.ValidatePromoCode(context.Background(), "SAVE20")
	require.ErrorIs(t, err, service.ErrPromoCodeWrongPurpose)
}

func TestValidateSubscriptionPromoRejectsInvalidState(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	tests := []struct {
		name      string
		promo     service.PromoCode
		usage     string
		wantError string
	}{
		{name: "zero rate", promo: service.PromoCode{Code: "ZERO", Purpose: service.PromoCodePurposeSubscriptionDiscount, DiscountRate: ptr(0.0), MaxUses: 1, Status: service.PromoCodeStatusActive}, wantError: "PROMO_CODE_INVALID_DISCOUNT"},
		{name: "full rate", promo: service.PromoCode{Code: "FULL", Purpose: service.PromoCodePurposeSubscriptionDiscount, DiscountRate: ptr(1.0), MaxUses: 1, Status: service.PromoCodeStatusActive}, wantError: "PROMO_CODE_INVALID_DISCOUNT"},
		{name: "disabled", promo: service.PromoCode{Code: "OFF", Purpose: service.PromoCodePurposeSubscriptionDiscount, DiscountRate: ptr(0.8), MaxUses: 1, Status: service.PromoCodeStatusDisabled}, wantError: "PROMO_CODE_DISABLED"},
		{name: "expired", promo: service.PromoCode{Code: "OLD", Purpose: service.PromoCodePurposeSubscriptionDiscount, DiscountRate: ptr(0.8), MaxUses: 1, Status: service.PromoCodeStatusActive, ExpiresAt: &past}, wantError: "PROMO_CODE_EXPIRED"},
		{name: "reserved", promo: service.PromoCode{Code: "HELD", Purpose: service.PromoCodePurposeSubscriptionDiscount, DiscountRate: ptr(0.8), MaxUses: 1, Status: service.PromoCodeStatusActive}, usage: service.PromoUsageStatusReserved, wantError: "PROMO_CODE_RESERVED"},
		{name: "consumed", promo: service.PromoCode{Code: "USED", Purpose: service.PromoCodePurposeSubscriptionDiscount, DiscountRate: ptr(0.8), MaxUses: 1, Status: service.PromoCodeStatusActive}, usage: service.PromoUsageStatusConsumed, wantError: "PROMO_CODE_CONSUMED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newPromoServiceTestHarness(t, tt.promo)
			if tt.usage != "" {
				_, err := h.client.PromoCodeUsage.Create().
					SetPromoCodeID(h.promoID).
					SetUserID(h.userID).
					SetBonusAmount(0).
					SetUsageType(service.PromoCodePurposeSubscriptionDiscount).
					SetStatus(tt.usage).
					Save(context.Background())
				require.NoError(t, err)
			}

			_, err := h.service.ValidateSubscriptionPromo(context.Background(), tt.promo.Code, 149)
			require.Error(t, err)
			require.Equal(t, tt.wantError, infraerrors.Reason(err))
		})
	}
}

func TestValidateSubscriptionPromoAllowsReleasedCode(t *testing.T) {
	h := newPromoServiceTestHarness(t, service.PromoCode{
		Code: "RETRY", Purpose: service.PromoCodePurposeSubscriptionDiscount,
		DiscountRate: ptr(0.8), MaxUses: 1, Status: service.PromoCodeStatusActive,
	})
	_, err := h.client.PromoCodeUsage.Create().
		SetPromoCodeID(h.promoID).
		SetUserID(h.userID).
		SetBonusAmount(0).
		SetUsageType(service.PromoCodePurposeSubscriptionDiscount).
		SetStatus(service.PromoUsageStatusReleased).
		Save(context.Background())
	require.NoError(t, err)

	got, err := h.service.ValidateSubscriptionPromo(context.Background(), "RETRY", 149)
	require.NoError(t, err)
	require.InDelta(t, 119.20, got.DiscountedAmount, 0.000001)
}

func TestCreateSubscriptionPromoDefinition(t *testing.T) {
	h := newPromoServiceTestHarness(t, service.PromoCode{
		Code: "SEED", Purpose: service.PromoCodePurposeRegistrationBonus,
		Status: service.PromoCodeStatusActive,
	})

	created, err := h.service.Create(context.Background(), &service.CreatePromoCodeInput{
		Code: " save20 ", Purpose: service.PromoCodePurposeSubscriptionDiscount,
		DiscountRate: ptr(0.8), BonusAmount: 99, MaxUses: 1,
	})
	require.NoError(t, err)
	require.Equal(t, "SAVE20", created.Code)
	require.Equal(t, service.PromoCodePurposeSubscriptionDiscount, created.Purpose)
	require.Zero(t, created.BonusAmount)
	require.Equal(t, 1, created.MaxUses)
}

func TestCreateSubscriptionPromoRejectsInvalidDefinition(t *testing.T) {
	tests := []struct {
		name      string
		rate      *float64
		maxUses   int
		wantError string
	}{
		{name: "zero rate", rate: ptr(0.0), maxUses: 1, wantError: "PROMO_CODE_INVALID_DISCOUNT"},
		{name: "full rate", rate: ptr(1.0), maxUses: 1, wantError: "PROMO_CODE_INVALID_DISCOUNT"},
		{name: "missing rate", maxUses: 1, wantError: "PROMO_CODE_INVALID_DISCOUNT"},
		{name: "multiple uses", rate: ptr(0.8), maxUses: 2, wantError: "PROMO_CODE_INVALID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newPromoServiceTestHarness(t, service.PromoCode{
				Code: "SEED", Purpose: service.PromoCodePurposeRegistrationBonus,
				Status: service.PromoCodeStatusActive,
			})
			_, err := h.service.Create(context.Background(), &service.CreatePromoCodeInput{
				Code: "NEW", Purpose: service.PromoCodePurposeSubscriptionDiscount,
				DiscountRate: tt.rate, MaxUses: tt.maxUses,
			})
			require.Error(t, err)
			require.Equal(t, tt.wantError, infraerrors.Reason(err))
		})
	}
}

func TestCreateRegistrationPromoRejectsDiscount(t *testing.T) {
	h := newPromoServiceTestHarness(t, service.PromoCode{
		Code: "SEED", Purpose: service.PromoCodePurposeRegistrationBonus,
		Status: service.PromoCodeStatusActive,
	})

	_, err := h.service.Create(context.Background(), &service.CreatePromoCodeInput{
		Code: "WELCOME", Purpose: service.PromoCodePurposeRegistrationBonus,
		DiscountRate: ptr(0.8), BonusAmount: 10,
	})
	require.ErrorIs(t, err, service.ErrPromoCodeInvalidDiscount)
}

func TestCreateRegistrationPromoKeepsLegacyDefaults(t *testing.T) {
	h := newPromoServiceTestHarness(t, service.PromoCode{
		Code: "SEED", Purpose: service.PromoCodePurposeRegistrationBonus,
		Status: service.PromoCodeStatusActive,
	})

	created, err := h.service.Create(context.Background(), &service.CreatePromoCodeInput{
		Code: " welcome ", BonusAmount: 10,
	})
	require.NoError(t, err)
	require.Equal(t, "WELCOME", created.Code)
	require.Equal(t, service.PromoCodePurposeRegistrationBonus, created.Purpose)

	validated, err := h.service.ValidatePromoCode(context.Background(), "welcome")
	require.NoError(t, err)
	require.Equal(t, created.ID, validated.ID)
}
