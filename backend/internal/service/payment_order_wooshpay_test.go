//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestWooshPaySelectedReturnURLValidation(t *testing.T) {
	for _, tc := range []struct {
		name, configured, requested, reason string
	}{
		{"both empty", "", "", "INVALID_RETURN_URL"},
		{"whitespace", " \t", " \n", "INVALID_RETURN_URL"},
		{"request fallback", "", "https://shop.example/payment/result", ""},
		{"configured destination", "https://shop.example/thanks?source=checkout", "", ""},
		{"invalid configured scheme", "javascript:alert(1)", "https://shop.example/payment/result", "INVALID_RETURN_URL"},
		{"relative configured URL", "/thanks", "", "INVALID_RETURN_URL"},
		{"userinfo", "https://user:pass@shop.example/thanks", "", "INVALID_RETURN_URL"},
		{"foreign request host", "", "https://other.example/payment/result", "INVALID_RETURN_URL"},
		{"invalid request path even with config", "https://shop.example/thanks", "https://shop.example/other", "INVALID_RETURN_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &PaymentService{}
			err := svc.validateSelectedCreateOrderInstance(context.Background(), CreateOrderRequest{
				ReturnURL: tc.requested, SrcHost: "shop.example",
			}, &payment.InstanceSelection{ProviderKey: payment.TypeWooshPay, Config: map[string]string{"successUrl": tc.configured}})
			if tc.reason == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, tc.reason, infraerrors.Reason(err))
				require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			}
		})
	}
	// Providers that do not need a redirect retain their existing behavior.
	require.NoError(t, (&PaymentService{}).validateSelectedCreateOrderInstance(context.Background(), CreateOrderRequest{}, &payment.InstanceSelection{ProviderKey: payment.TypeAlipay}))
}

type wooshPaySelectionStub struct{ payment.LoadBalancer }

func (wooshPaySelectionStub) SelectInstance(context.Context, string, payment.PaymentType, payment.Strategy, float64) (*payment.InstanceSelection, error) {
	// No API credentials: this test must never reach provider construction.
	return &payment.InstanceSelection{ProviderKey: payment.TypeWooshPay, Config: map[string]string{"currency": "CNY"}}, nil
}

func TestWooshPayMissingReturnURLDoesNotCreateOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("buyer@example.com").SetPasswordHash("test").Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{
		entClient:     client,
		configService: &PaymentConfigService{entClient: client, settingRepo: &paymentConfigSettingRepoStub{values: map[string]string{SettingPaymentEnabled: "true"}}},
		userRepo:      &mockUserRepo{getByIDUser: &User{ID: user.ID, Email: user.Email, Status: payment.EntityStatusActive}},
		loadBalancer:  wooshPaySelectionStub{},
	}
	_, err = svc.CreateOrder(ctx, CreateOrderRequest{UserID: user.ID, Amount: 10, PaymentType: payment.TypeWooshPay})
	require.Equal(t, "INVALID_RETURN_URL", infraerrors.Reason(err))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Zero(t, client.PaymentOrder.Query().CountX(ctx))
}
