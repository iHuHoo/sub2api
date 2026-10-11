//go:build unit

package handler

import (
	"bytes"
	"context"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

type supplierImportRepo struct {
	service.SupplierRepository
	account *service.Account
	owner   int64
}

func (r *supplierImportRepo) CreateAccount(_ context.Context, owner int64, a *service.Account) error {
	r.owner = owner
	a.ID = 7
	r.account = a
	return nil
}
func (r *supplierImportRepo) GetAccount(_ context.Context, owner, id int64) (*service.Account, error) {
	if owner != r.owner {
		return nil, service.ErrSupplierUnavailable
	}
	return r.account, nil
}
func TestSupplierCodexImportCreatesOnlyOwnedAccount(t *testing.T) {
	repo := &supplierImportRepo{}
	h := NewSupplierHandler(service.NewSupplierService(repo, nil))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 11})
	c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"mine","content":"{\"tokens\":{\"access_token\":\"import-secret\",\"refresh_token\":\"rt\"},\"group_ids\":[99],\"priority\":999}"}`))
	h.ImportCodexSession(c)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.EqualValues(t, 11, repo.owner)
	require.EqualValues(t, 11, *repo.account.SupplierUserID)
	require.Empty(t, repo.account.GroupIDs)
	require.Equal(t, 1, repo.account.Concurrency)
	require.Equal(t, "import-secret", repo.account.Credentials["access_token"])
	require.NotContains(t, w.Body.String(), "import-secret")
	require.Contains(t, w.Body.String(), `"created":1`)
}

func TestSupplierCodexImportRejectsManagementFields(t *testing.T) {
	h := NewSupplierHandler(nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 11})
	c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"content":"secret","group_ids":[1]}`))
	h.ImportCodexSession(c)
	require.Equal(t, 400, w.Code)
	require.NotContains(t, w.Body.String(), "secret")
}
