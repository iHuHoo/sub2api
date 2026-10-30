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
	"time"
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

type supplierImportPrivacy struct {
	service.SupplierPrivacyService
	calls chan int64
}

func (p *supplierImportPrivacy) EnsureOpenAIPrivacy(ctx context.Context, a *service.Account) string {
	if ctx.Done() != nil {
		panic("privacy context must be detached")
	}
	p.calls <- a.ID
	return "training_disabled"
}

func TestSupplierCodexImportCreatesOnlyOwnedAccount(t *testing.T) {
	repo := &supplierImportRepo{}
	privacy := &supplierImportPrivacy{calls: make(chan int64, 2)}
	h := NewSupplierHandler(service.NewSupplierService(repo, nil, privacy))
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
	select {
	case id := <-privacy.calls:
		require.EqualValues(t, 7, id)
	case <-time.After(time.Second):
		t.Fatal("Codex import did not invoke supplier privacy")
	}
	select {
	case <-privacy.calls:
		t.Fatal("duplicate privacy invocation")
	case <-time.After(50 * time.Millisecond):
	}
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
