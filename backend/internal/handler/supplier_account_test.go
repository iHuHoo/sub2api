//go:build unit

package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type supplierAccountTestRunnerStub struct {
	modelCalls int
	testCalls  int
}

func (s *supplierAccountTestRunnerStub) SupplierAvailableModels(context.Context, int64, int64) ([]service.AccountTestModel, error) {
	s.modelCalls++
	return []service.AccountTestModel{{ID: "model-1", DisplayName: "Model 1"}}, nil
}

func (s *supplierAccountTestRunnerStub) TestSupplierAccountConnection(c *gin.Context, _ int64, _ int64, modelID string) error {
	s.testCalls++
	_, _ = c.Writer.Write([]byte(`data: {"type":"test_complete","model":"` + modelID + `","success":true}` + "\n\n"))
	return nil
}

func supplierAccountDiagnosticRequest(method string, account *service.Account, runner *supplierAccountTestRunnerStub, owner int64, body string) (*SupplierHandler, *httptest.ResponseRecorder, *gin.Context) {
	h := NewSupplierHandler(service.NewSupplierService(&supplierUsageRepo{account: account}, nil, nil))
	h.accountTest = runner
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: owner})
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	c.Request = httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	return h, w, c
}

func TestSupplierAccountDiagnosticsRejectUnavailableAccountsBeforeServiceCalls(t *testing.T) {
	owner := int64(11)
	foreign := int64(22)
	for _, tc := range []struct {
		name    string
		account *service.Account
	}{
		{name: "foreign", account: &service.Account{ID: 7, SupplierUserID: &foreign}},
		{name: "administrator", account: &service.Account{ID: 7}},
		{name: "missing", account: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				runner := &supplierAccountTestRunnerStub{}
				h, w, c := supplierAccountDiagnosticRequest(method, tc.account, runner, owner, `{"model_id":"model-1"}`)
				if method == http.MethodGet {
					h.GetAccountModels(c)
				} else {
					h.TestAccount(c)
				}
				require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
				require.Zero(t, runner.modelCalls)
				require.Zero(t, runner.testCalls)
			}
		})
	}
}

func TestSupplierAccountDiagnosticsUseOwnedAccountAndSafeModelView(t *testing.T) {
	owner := int64(11)
	account := &service.Account{ID: 7, SupplierUserID: &owner}
	runner := &supplierAccountTestRunnerStub{}
	h, w, c := supplierAccountDiagnosticRequest(http.MethodGet, account, runner, owner, "")

	h.GetAccountModels(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, runner.modelCalls)
	require.JSONEq(t, `{"code":0,"message":"success","data":[{"id":"model-1","display_name":"Model 1"}]}`, w.Body.String())

	h, w, c = supplierAccountDiagnosticRequest(http.MethodPost, account, runner, owner, `{"model_id":"model-1"}`)
	h.TestAccount(c)
	require.Equal(t, 1, runner.testCalls)
	require.Contains(t, w.Body.String(), `"success":true`)
}

func TestSupplierAccountHandlerDoesNotRecoverAccountState(t *testing.T) {
	source, err := os.ReadFile("supplier_handler.go")
	require.NoError(t, err)
	require.NotContains(t, string(source), "RecoverAccountAfterSuccessfulTest")
}

func TestSupplierAccountTestRejectsNonModelFields(t *testing.T) {
	owner := int64(11)
	account := &service.Account{ID: 7, SupplierUserID: &owner}
	for _, field := range []string{"prompt", "mode", "image_data_url", "audio_data_url"} {
		t.Run(field, func(t *testing.T) {
			runner := &supplierAccountTestRunnerStub{}
			body := `{"model_id":"model-1","` + field + `":"injected-secret"}`
			h, w, c := supplierAccountDiagnosticRequest(http.MethodPost, account, runner, owner, body)

			h.TestAccount(c)

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Zero(t, runner.testCalls)
			require.NotContains(t, w.Body.String(), "injected-secret")
		})
	}
}

func TestSupplierAccountDiagnosticInvalidIDIsUnavailable(t *testing.T) {
	runner := &supplierAccountTestRunnerStub{}
	h, w, c := supplierAccountDiagnosticRequest(http.MethodGet, nil, runner, 11, "")
	c.Params[0].Value = strconv.FormatInt(0, 10)
	h.GetAccountModels(c)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Zero(t, runner.modelCalls)
}
