//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type supplierUsageRepo struct {
	service.SupplierRepository
	account *service.Account
}

func (r *supplierUsageRepo) GetAccount(_ context.Context, owner, id int64) (*service.Account, error) {
	if r.account == nil || r.account.ID != id || r.account.SupplierUserID == nil || *r.account.SupplierUserID != owner {
		return nil, service.ErrSupplierUnavailable
	}
	return r.account, nil
}

type supplierUsageStub struct {
	usage        *service.UsageInfo
	err          error
	activeCalls  int
	passiveCalls int
}

func (s *supplierUsageStub) GetUsage(context.Context, int64, ...bool) (*service.UsageInfo, error) {
	s.activeCalls++
	return s.usage, s.err
}

func (s *supplierUsageStub) GetPassiveUsage(context.Context, int64) (*service.UsageInfo, error) {
	s.passiveCalls++
	return s.usage, s.err
}

func supplierUsageRequest(t *testing.T, account *service.Account, usage *supplierUsageStub, owner int64) *httptest.ResponseRecorder {
	t.Helper()
	h := NewSupplierHandler(service.NewSupplierService(&supplierUsageRepo{account: account}, nil, nil))
	h.accountUsage = usage
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: owner})
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(account.ID, 10)}}
	c.Request = httptest.NewRequest("GET", "/", nil)
	h.GetAccountUsage(c)
	return w
}

func TestSupplierAccountUsageReturnsOnlySafeWindowFields(t *testing.T) {
	owner := int64(11)
	reset := time.Date(2026, 10, 29, 1, 2, 3, 0, time.UTC)
	usage := &supplierUsageStub{usage: &service.UsageInfo{
		Source:    "passive",
		UpdatedAt: &reset,
		FiveHour: &service.UsageProgress{
			Utilization:      42,
			ResetsAt:         &reset,
			RemainingSeconds: 3600,
			WindowStats:      &service.WindowStats{Requests: 7, Tokens: 1234, Cost: 9.9, StandardCost: 10.1, UserCost: 12.3},
		},
		ErrorCode:           "rate_limited",
		Error:               "upstream secret response",
		ForbiddenReason:     "internal account error",
		ValidationURL:       "https://private.example/verify",
		SubscriptionTierRaw: "private-tier",
		ModelForwardingRules: map[string]string{
			"private-model": "other-model",
		},
	}}
	account := &service.Account{ID: 7, SupplierUserID: &owner, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth}

	w := supplierUsageRequest(t, account, usage, owner)

	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, 1, usage.passiveCalls)
	require.Zero(t, usage.activeCalls)
	require.Contains(t, w.Body.String(), `"utilization":42`)
	require.Contains(t, w.Body.String(), `"error_code":"rate_limited"`)
	for _, private := range []string{"upstream secret", "internal account error", "private.example", "private-tier", "private-model", "window_stats", "cost", "standard_cost", "user_cost"} {
		require.NotContains(t, w.Body.String(), private)
	}
}

func TestSupplierAccountUsageNormalizesErrorCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "quota not sampled", in: "quota_unknown", want: ""},
		{name: "rate limited", in: "rate_limited", want: "rate_limited"},
		{name: "unauthenticated", in: "unauthenticated", want: "unauthenticated"},
		{name: "forbidden", in: "forbidden", want: "forbidden"},
		{name: "network error", in: "network_error", want: "network_error"},
		{name: "unavailable", in: "unavailable", want: "unavailable"},
		{name: "unknown", in: "dynamic upstream detail", want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := supplierAccountUsageView(&service.UsageInfo{ErrorCode: tc.in})
			require.Equal(t, tc.want, got.ErrorCode)
		})
	}
}

func TestSupplierAccountUsageRejectsForeignAndAdminAccountsBeforeUsageLookup(t *testing.T) {
	for _, supplierUserID := range []*int64{nil, func() *int64 { id := int64(22); return &id }()} {
		usage := &supplierUsageStub{usage: &service.UsageInfo{}}
		account := &service.Account{ID: 8, SupplierUserID: supplierUserID, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth}

		w := supplierUsageRequest(t, account, usage, 11)

		require.Equal(t, 404, w.Code, w.Body.String())
		require.Zero(t, usage.passiveCalls)
		require.Zero(t, usage.activeCalls)
	}
}

func TestSupplierAccountUsageDegradesLookupFailureWithoutLeakingError(t *testing.T) {
	owner := int64(11)
	usage := &supplierUsageStub{err: errors.New("upstream response contains secret")}
	account := &service.Account{ID: 9, SupplierUserID: &owner, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}

	w := supplierUsageRequest(t, account, usage, owner)

	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, 1, usage.activeCalls)
	require.Contains(t, w.Body.String(), `"error_code":"unavailable"`)
	require.NotContains(t, w.Body.String(), "upstream response")
}
