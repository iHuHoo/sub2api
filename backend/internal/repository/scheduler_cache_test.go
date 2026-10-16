package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFilterSchedulerCredentialsKeepsSubscriptionPlanType(t *testing.T) {
	filtered := filterSchedulerCredentials(map[string]any{
		"plan_type":     "plus",
		"access_token":  "secret-access-token",
		"refresh_token": "secret-refresh-token",
	})

	require.Equal(t, "plus", filtered["plan_type"])
	require.NotContains(t, filtered, "access_token")
	require.NotContains(t, filtered, "refresh_token")
}

func TestFilterSchedulerCredentialsKeepsGeminiExplicitCacheKeys(t *testing.T) {
	filtered := filterSchedulerCredentials(map[string]any{
		"api_key":              "k",
		"base_url":             "https://gw.example.com",
		"auth_mode":            "apikey",
		"service_account_json": "secret",
		service.GeminiExplicitCacheUpstreamCredentialKey:       true,
		service.GeminiExplicitCacheUpstreamMaxTTLCredentialKey: 3600,
	})

	require.Equal(t, "https://gw.example.com", filtered["base_url"])
	require.Equal(t, "apikey", filtered["auth_mode"])
	require.Equal(t, true, filtered[service.GeminiExplicitCacheUpstreamCredentialKey])
	require.Equal(t, 3600, filtered[service.GeminiExplicitCacheUpstreamMaxTTLCredentialKey])
	require.NotContains(t, filtered, "service_account_json")
}

func TestSchedulerMetadataAccountKeepsOpenAISubscriptionIdentity(t *testing.T) {
	account := service.Account{
		ID:       24,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"plan_type":    "plus",
			"access_token": "secret-access-token",
		},
	}

	metadata := buildSchedulerMetadataAccount(account)

	require.True(t, metadata.IsOpenAIChatGPTSubscription())
	require.Empty(t, metadata.GetCredential("access_token"))
}

func TestSchedulerMetadataAccountProjectsUpstreamBillingProbe(t *testing.T) {
	lastError := strings.Repeat("upstream diagnostic ", 512)
	probe := map[string]any{
		"status": "ok",
		"data": map[string]any{
			"billing_scope":             "token",
			"resolved_rate_multiplier":  0.03,
			"peak_rate_enabled":         true,
			"peak_start":                "09:00",
			"peak_end":                  "18:00",
			"peak_rate_multiplier":      2.0,
			"timezone":                  "Asia/Shanghai",
			"effective_rate_multiplier": 0.03,
			"remote_diagnostic":         lastError,
		},
		"received_at":   "2026-07-13T10:00:00Z",
		"fresh_until":   "2026-07-13T11:00:00Z",
		"next_probe_at": "2026-07-13T10:30:00Z",
		"http_status":   502,
		"last_error":    lastError,
	}
	account := service.Account{
		ID: 42,
		Extra: map[string]any{
			"upstream_billing_probe": probe,
			"unused_large_field":     "drop-me",
		},
	}

	metadata := buildSchedulerMetadataAccount(account)
	fullPayload, metaPayload, err := marshalSchedulerCacheAccount(account)
	require.NoError(t, err)

	filtered, ok := metadata.Extra["upstream_billing_probe"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "ok", filtered["status"])
	require.Equal(t, "2026-07-13T10:00:00Z", filtered["received_at"])
	require.Equal(t, "2026-07-13T11:00:00Z", filtered["fresh_until"])
	require.Equal(t, "2026-07-13T10:30:00Z", filtered["next_probe_at"])
	require.NotContains(t, filtered, "http_status")
	require.NotContains(t, filtered, "last_error")
	filteredData, ok := filtered["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "token", filteredData["billing_scope"])
	require.Equal(t, 0.03, filteredData["resolved_rate_multiplier"])
	require.Equal(t, true, filteredData["peak_rate_enabled"])
	require.Equal(t, "09:00", filteredData["peak_start"])
	require.Equal(t, "18:00", filteredData["peak_end"])
	require.Equal(t, 2.0, filteredData["peak_rate_multiplier"])
	require.Equal(t, "Asia/Shanghai", filteredData["timezone"])
	require.NotContains(t, filteredData, "effective_rate_multiplier")
	require.NotContains(t, filteredData, "remote_diagnostic")
	require.NotContains(t, metadata.Extra, "unused_large_field")
	require.Contains(t, string(fullPayload), lastError)
	require.NotContains(t, string(metaPayload), "last_error")
	require.Less(t, len(metaPayload)*4, len(fullPayload))
}

func TestSchedulerMetadataAccountDropsInvalidUpstreamBillingProbe(t *testing.T) {
	for _, probe := range []any{
		"invalid",
		map[string]any{},
		map[string]any{"status": ""},
	} {
		metadata := buildSchedulerMetadataAccount(service.Account{
			Extra: map[string]any{service.UpstreamBillingProbeExtraKey: probe},
		})

		require.NotContains(t, metadata.Extra, service.UpstreamBillingProbeExtraKey)
	}
}

func TestSchedulerMetadataProxyBindingRoundTrip(t *testing.T) {
	id := int64(9)
	for _, a := range []service.Account{{ID: 1, ProxyID: &id}, {ID: 2}} {
		full, meta, err := marshalSchedulerCacheAccount(a)
		require.NoError(t, err)
		for _, payload := range [][]byte{full, meta} {
			var got service.Account
			require.NoError(t, json.Unmarshal(payload, &got))
			require.Equal(t, a.ProxyID, got.ProxyID)
		}
	}
}

type assignmentBoundaryCache struct {
	service.SchedulerCache
	snapshot []*service.Account
}

func (c *assignmentBoundaryCache) GetSnapshot(context.Context, service.SchedulerBucket) ([]*service.Account, bool, error) {
	return c.snapshot, true, nil
}
func (c *assignmentBoundaryCache) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	for _, a := range c.snapshot {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, nil
}

type assignmentBoundaryRepo struct {
	service.AccountRepository
	calls     int
	err       error
	valid     bool
	batchSize int
	expired   bool
}

func (r *assignmentBoundaryRepo) RefreshAccountProxies(_ context.Context, accounts []*service.Account) error {
	r.calls++
	r.batchSize = len(accounts)
	if r.err != nil {
		return r.err
	}
	id := int64(9)
	for _, a := range accounts {
		if a.ID == 1 {
			a.SupplierUserID = &id
			a.ProxyID = &id
			a.Proxy = &service.Proxy{Status: service.StatusActive, AssignedSupplierUserID: &id, AssignmentValid: r.valid}
			if r.expired {
				past := time.Now().Add(-time.Hour)
				a.Proxy.ExpiresAt = &past
			}
		}
	}
	return nil
}
func TestProductionSchedulerCacheAssignmentBoundary(t *testing.T) {
	ctx := context.Background()
	id := int64(9)
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			cache := &assignmentBoundaryCache{}
			repo := &assignmentBoundaryRepo{}
			for _, a := range []service.Account{{ID: 1, Status: service.StatusActive, Schedulable: true, ProxyID: &id}, {ID: 2, Status: service.StatusActive, Schedulable: true}} {
				_, payload, err := marshalSchedulerCacheAccount(a)
				require.NoError(t, err)
				if legacy {
					var old map[string]any
					require.NoError(t, json.Unmarshal(payload, &old))
					delete(old, "ProxyID")
					payload, err = json.Marshal(old)
					require.NoError(t, err)
				}
				var decoded service.Account
				require.NoError(t, json.Unmarshal(payload, &decoded))
				cache.snapshot = append(cache.snapshot, &decoded)
			}
			svc := service.NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
			got, _, err := svc.ListSchedulableAccounts(ctx, nil, service.PlatformOpenAI, false)
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, int64(2), got[0].ID)
			require.Equal(t, 1, repo.calls)
			require.Equal(t, 2, repo.batchSize)
			repo.valid = true
			got, _, err = svc.ListSchedulableAccounts(ctx, nil, service.PlatformOpenAI, false)
			require.NoError(t, err)
			require.Len(t, got, 2)
			repo.expired = true
			got, _, err = svc.ListSchedulableAccounts(ctx, nil, service.PlatformOpenAI, false)
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, int64(2), got[0].ID)
			repo.expired = false
			repo.err = errors.New("unavailable")
			_, _, err = svc.ListSchedulableAccounts(ctx, nil, service.PlatformOpenAI, false)
			require.Error(t, err)
			_, err = svc.GetAccount(ctx, 1)
			require.Error(t, err)
			missing := service.NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
			_, _, err = missing.ListSchedulableAccounts(ctx, nil, service.PlatformOpenAI, false)
			require.Error(t, err)
			_, err = missing.GetAccount(ctx, 1)
			require.Error(t, err)
		})
	}
}
