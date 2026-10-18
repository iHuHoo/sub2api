//go:build unit

package service

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
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
		{"configured destination", "https://trusted.example/payment/result", "", ""},
		{"valid request ignores unused config", "javascript:alert(1)", "https://shop.example/payment/result", ""},
		{"invalid configured scheme", "javascript:alert(1)", "", "INVALID_RETURN_URL"},
		{"invalid configured path", "https://shop.example/thanks", "", "INVALID_RETURN_URL"},
		{"relative configured URL", "/thanks", "", "INVALID_RETURN_URL"},
		{"userinfo", "https://user:pass@shop.example/thanks", "", "INVALID_RETURN_URL"},
		{"foreign request host", "", "https://other.example/payment/result", "INVALID_RETURN_URL"},
		{"foreign host must not fall back", "https://trusted.example/payment/result", "https://other.example/payment/result", "INVALID_RETURN_URL"},
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

func TestWooshPayReturnURLRecoveryForRequestAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, requested, configured, wantHost string
	}{
		{"request wins", "https://shop.example/payment/result?order_id=999&resume_token=untrusted", "https://fallback.example/payment/result", "shop.example"},
		{"missing request", "", "https://fallback.example/payment/result?order_id=999&resume_token=untrusted#fragment", "fallback.example"},
		{"whitespace request", " \t", "https://fallback.example/payment/result", "fallback.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &PaymentService{resumeService: NewPaymentResumeService([]byte("test-signing-key"))}
			req := CreateOrderRequest{PaymentType: payment.TypeWooshPay, ReturnURL: tc.requested, SrcHost: "shop.example"}
			sel := &payment.InstanceSelection{ProviderKey: payment.TypeWooshPay, InstanceID: "1", Config: map[string]string{"successUrl": tc.configured}}
			prepared, token, err := svc.buildSelectedPaymentReturnURL(&dbent.PaymentOrder{ID: 42, UserID: 7, OutTradeNo: "order-42"}, req, sel)
			require.NoError(t, err)
			actual, err := provider.ResolveWooshPaySuccessURL(sel.Config, prepared)
			require.NoError(t, err)
			u, err := url.Parse(actual)
			require.NoError(t, err)
			require.Equal(t, tc.wantHost, u.Host)
			require.Equal(t, "/payment/result", u.Path)
			require.Empty(t, u.Fragment)
			require.Equal(t, "42", u.Query().Get("order_id"))
			require.Equal(t, "order-42", u.Query().Get("out_trade_no"))
			require.Equal(t, "success", u.Query().Get("status"))
			require.NotEmpty(t, token)
			require.Equal(t, token, u.Query().Get("resume_token"))
			claims, err := svc.paymentResume().ParseToken(token)
			require.NoError(t, err)
			require.Equal(t, int64(42), claims.OrderID)
			require.Equal(t, int64(7), claims.UserID)
			require.Equal(t, "https://"+tc.wantHost+"/payment/result", claims.CanonicalReturnURL)
		})
	}
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
