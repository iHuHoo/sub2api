//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type supplierAccountTestRepo struct {
	AccountRepository
	account    *Account
	stateCalls int
}

func (r *supplierAccountTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return r.account, nil
}

func (r *supplierAccountTestRepo) SetError(context.Context, int64, string) error {
	r.stateCalls++
	return nil
}

type supplierAccountTestUpstream struct{}

func (supplierAccountTestUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return supplierAccountTestResponse(), nil
}

func (supplierAccountTestUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	return supplierAccountTestResponse(), nil
}

func supplierAccountTestResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":"upstream-secret","api_key":"credential-secret"}`)),
	}
}

func TestSupplierAccountConnectionIsReadOnlyAndSanitizesSSE(t *testing.T) {
	owner := int64(11)
	account := &Account{
		ID:             7,
		Name:           "owned",
		Platform:       PlatformAnthropic,
		Type:           AccountTypeAPIKey,
		Status:         StatusActive,
		Schedulable:    true,
		SupplierUserID: &owner,
		Credentials:    map[string]any{"api_key": "credential-secret"},
	}
	repo := &supplierAccountTestRepo{account: account}
	svc := NewAccountTestService(repo, nil, nil, nil, nil, supplierAccountTestUpstream{}, &config.Config{}, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	err := svc.TestSupplierAccountConnection(c, owner, account.ID, "claude-sonnet-4-5")

	require.Error(t, err)
	require.Zero(t, repo.stateCalls)
	require.Equal(t, StatusActive, account.Status)
	require.True(t, account.Schedulable)
	require.Contains(t, w.Body.String(), `"code":"ACCOUNT_TEST_FAILED"`)
	require.Contains(t, w.Body.String(), `"error":"Account test failed"`)
	require.NotContains(t, w.Body.String(), "upstream-secret")
	require.NotContains(t, w.Body.String(), "credential-secret")
}

func TestSupplierAccountConnectionSuccessDoesNotRecoverState(t *testing.T) {
	owner := int64(11)
	account := &Account{
		ID: 9, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusError,
		Schedulable: false, SupplierUserID: &owner, Extra: map[string]any{"synthetic_ui_test": true},
	}
	repo := &supplierAccountTestRepo{account: account}
	svc := NewAccountTestService(repo, nil, nil, nil, nil, nil, &config.Config{}, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	require.NoError(t, svc.TestSupplierAccountConnection(c, owner, account.ID, "claude-sonnet-4-5"))
	require.Equal(t, StatusError, account.Status)
	require.False(t, account.Schedulable)
	require.Zero(t, repo.stateCalls)
	require.Contains(t, w.Body.String(), `"success":true`)
}

func TestSupplierAvailableModelsReturnsOnlyIDAndDisplayName(t *testing.T) {
	owner := int64(11)
	repo := &supplierAccountTestRepo{account: &Account{
		ID: 8, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, SupplierUserID: &owner,
		Credentials: map[string]any{"model_mapping": map[string]any{"public-model": "private-upstream-model"}},
	}}
	svc := NewAccountTestService(repo, nil, nil, nil, nil, nil, &config.Config{}, nil)

	models, err := svc.SupplierAvailableModels(context.Background(), owner, 8)

	require.NoError(t, err)
	require.Equal(t, []AccountTestModel{{ID: "public-model", DisplayName: "public-model"}}, models)
}
