//go:build unit

package handler

import (
	"bytes"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestSupplierStrictBindingRejectsPrivilegedAndTrailingFields(t *testing.T) {
	for _, body := range []string{`{"name":"a","priority":100}`, `{"name":"a","supplier_user_id":22}`, `{"name":"a","extra":{"x":"y"}}`, `{"name":"a"} {"name":"b"}`, `{"name":"a","credentials":{"api_key":"secret"},"groups":[1]}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
		var in service.SupplierAccountCreate
		require.False(t, bindSupplierJSON(c, &in), body)
		require.Equal(t, 400, w.Code)
		require.NotContains(t, w.Body.String(), "secret")
	}
}
func TestSupplierErrorWhitelist(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	supplierError(c, fmt.Errorf("downstream-secret"))
	require.Equal(t, 500, w.Code)
	require.NotContains(t, w.Body.String(), "downstream-secret")
	for _, err := range []error{service.ErrSupplierUnavailable, service.ErrSupplierProxyInUse, service.ErrSupplierInvalid, service.ErrSupplierReadOnly} {
		w = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(w)
		supplierError(c, err)
		require.NotEqual(t, 500, w.Code)
	}
}
