package routes

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type clashStepSettings struct {
	service.SettingRepository
	enabled bool
}

func (s clashStepSettings) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyStepUpEnabled && s.enabled {
		return "true", nil
	}
	return "", service.ErrSettingNotFound
}

type clashStepUsers struct{ service.UserRepository }

func (clashStepUsers) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 1, TotpEnabled: true}, nil
}
func (clashStepUsers) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type clashStepCache struct {
	service.TotpCache
	granted bool
}

func (s clashStepCache) HasStepUpGrant(_ context.Context, id int64, session string) (bool, error) {
	return s.granted && id == 1 && session == "fixture-session", nil
}

type clashStepStore struct {
	service.ClashSubscriptionRepository
	record *service.ProxySubscription
	writes int
}

func (s *clashStepStore) WithLockedOwner(_ context.Context, _ service.SubscriptionActor, fn func(service.ClashSubscriptionTx) error) error {
	return fn(s)
}
func (s *clashStepStore) Get(context.Context) (*service.ProxySubscription, error) {
	return s.record, nil
}
func (s *clashStepStore) Proxies(context.Context) ([]service.Proxy, error) {
	return []service.Proxy{{ID: 1, Protocol: "http", Host: "example.test", Port: 80}}, nil
}
func (s *clashStepStore) Create(_ context.Context, record *service.ProxySubscription) error {
	s.writes++
	record.Version = 1
	s.record = record
	return nil
}
func (s *clashStepStore) Reset(_ context.Context, record *service.ProxySubscription, version int64) error {
	s.writes++
	record.Version = version + 1
	s.record = record
	return nil
}
func (s *clashStepStore) Revoke(context.Context, int64) error { s.writes++; return nil }

func TestClashSubscriptionStepUp(t *testing.T) {
	for _, role := range []string{"admin", "supplier"} {
		for _, action := range []string{"create", "reset", "metadata", "revoke"} {
			for _, state := range []string{"default-off", "missing-grant", "granted"} {
				t.Run(role+"/"+action+"/"+state, func(t *testing.T) {
					settings := service.NewSettingService(clashStepSettings{enabled: state != "default-off"}, &config.Config{})
					users := service.NewUserService(clashStepUsers{}, nil, nil, nil)
					totp := service.NewTotpService(nil, nil, clashStepCache{granted: state == "granted"}, settings, nil, nil)
					step := middleware.NewStepUpAuthMiddleware(totp, users, settings)
					store := &clashStepStore{}
					if action != "create" {
						store.record = &service.ProxySubscription{Version: 7, TokenHash: "previous-hash", ExpiresAt: time.Now().Add(time.Hour)}
					}
					svc := service.NewClashSubscriptionService(store, &config.Config{ClashSubscription: config.ClashSubscriptionConfig{Enabled: true, PublicBaseURL: "https://api.example"}})
					h := &handler.Handlers{ClashSubscription: handler.NewClashSubscriptionHandler(svc, nil)}
					jwt := middleware.JWTAuthMiddleware(func(c *gin.Context) {
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
						c.Set(string(middleware.ContextKeyUserRole), role)
						c.Set(middleware.ContextKeySessionID, "fixture-session")
						c.Next()
					})
					r := gin.New()
					RegisterClashSubscriptionRoutes(r.Group("/api/v1"), h, jwt, middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), middleware.NewPanelRateLimiter(nil, nil), nil, step)
					method, path := "POST", "/api/v1/"+role+"/clash-subscription"
					switch action {
					case "reset":
						path += "/reset"
					case "metadata":
						method = "GET"
					case "revoke":
						method = "DELETE"
					}
					req := httptest.NewRequest(method, path, strings.NewReader(`{"days":30,"expected_version":7}`))
					req.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					r.ServeHTTP(w, req)
					if state == "missing-grant" && (action == "create" || action == "reset") {
						require.Equal(t, 403, w.Code, w.Body.String())
						require.Contains(t, w.Body.String(), "STEP_UP_REQUIRED")
						require.NotContains(t, w.Body.String(), "subscription_url")
						require.Zero(t, store.writes)
						if action == "create" {
							require.Nil(t, store.record)
						} else {
							require.EqualValues(t, 7, store.record.Version)
							require.Equal(t, "previous-hash", store.record.TokenHash)
						}
					} else {
						require.Equal(t, 200, w.Code, w.Body.String())
						if action == "create" || action == "reset" {
							require.Equal(t, 1, store.writes)
							require.Contains(t, w.Body.String(), "subscription_url")
						}
					}
				})
			}
		}
	}
}

func (f *clashStepStore) ReadMetadataSnapshot(ctx context.Context, a service.SubscriptionActor, fn func(service.ClashSubscriptionTx) error) error {
	return f.WithLockedOwner(ctx, a, fn)
}
