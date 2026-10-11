//go:build unit

package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type supplierAuthUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r *supplierAuthUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}

type supplierAuthRefreshCache struct {
	userHandlerRefreshTokenCacheStub
	data map[string]*service.RefreshTokenData
}

func (r *supplierAuthRefreshCache) StoreRefreshToken(_ context.Context, hash string, data *service.RefreshTokenData, _ time.Duration) error {
	r.data[hash] = data
	return nil
}
func (r *supplierAuthRefreshCache) GetRefreshToken(_ context.Context, hash string) (*service.RefreshTokenData, error) {
	return r.data[hash], nil
}

func TestSupplierBackendModeLoginAndRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "supplier-auth-test-secret", AccessTokenExpireMinutes: 60, RefreshTokenExpireDays: 7}}
	settings := service.NewSettingService(&oauthPendingFlowSettingRepoStub{values: map[string]string{service.SettingKeyBackendModeEnabled: "true"}}, cfg)
	require.NoError(t, settings.UpdateSettings(context.Background(), &service.SystemSettings{BackendModeEnabled: true}))
	user := &service.User{ID: 7, Role: service.RoleSupplier, Status: service.StatusActive, Email: "supplier@test.com"}
	repo := &supplierAuthUserRepo{user: user}
	cache := &supplierAuthRefreshCache{data: map[string]*service.RefreshTokenData{}}
	auth := service.NewAuthService(nil, repo, nil, cache, cfg, settings, nil, nil, nil, nil, nil, nil, nil)
	h := NewAuthHandler(cfg, auth, nil, settings, nil, nil, nil, nil)
	require.NoError(t, h.ensureBackendModeAllowsUser(context.Background(), user))
	require.Error(t, h.ensureBackendModeAllowsUser(context.Background(), &service.User{Role: service.RoleUser}))
	pair, err := auth.GenerateTokenPair(context.Background(), user, "")
	require.NoError(t, err)
	r := gin.New()
	r.POST("/api/v1/auth/refresh", h.RefreshToken)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewBufferString(`{"refresh_token":"`+pair.RefreshToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}
