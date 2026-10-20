package routes

import (
	"context"
	"encoding/base64"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

type clashDownloadStore struct{ calls int }

func (f *clashDownloadStore) WithLockedOwner(context.Context, service.SubscriptionActor, func(service.ClashSubscriptionTx) error) error {
	return service.ErrClashForbidden
}
func (f *clashDownloadStore) ReadDownloadSnapshot(_ context.Context, hash string, fn func(*service.ProxySubscription, []service.Proxy) error) error {
	f.calls++
	if hash != service.ClashTokenHash(base64.RawURLEncoding.EncodeToString(make([]byte, 32))) {
		return service.ErrClashNotFound
	}
	return fn(&service.ProxySubscription{ID: 1}, []service.Proxy{{ID: 1, Protocol: "http", Host: "example.test", Port: 80, Password: "fixture-password"}})
}
func TestClashSubscriptionDownloadCORSAndLimits(t *testing.T) {
	for _, origins := range [][]string{{"*"}, {"https://aoxtoken.com"}} {
		t.Run(origins[0], func(t *testing.T) {
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			repo := &clashDownloadStore{}
			svc := service.NewClashSubscriptionService(repo, &config.Config{ClashSubscription: config.ClashSubscriptionConfig{Enabled: true, PublicBaseURL: "https://api.example"}})
			h := handler.NewClashSubscriptionHandler(svc, middleware.NewClashSubscriptionLimiter(client))
			r := gin.New()
			r.Use(middleware.CORS(config.CORSConfig{AllowedOrigins: origins}))
			r.Any("/api/v1/clash/subscription", h.Download)
			serve := func(method, host, token string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "https://"+host+"/api/v1/clash/subscription?token="+token, nil)
				req.Header.Set("Origin", "https://aoxtoken.com")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
				require.Contains(t, w.Header().Get("Cache-Control"), "no-store")
				return w
			}
			token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			require.Equal(t, 404, serve("GET", "evil.example", token).Code)
			require.Equal(t, 405, serve("POST", "api.example", token).Code)
			require.Equal(t, 404, serve("GET", "api.example", "invalid").Code)
			for i := 0; i < 10; i++ {
				w := serve("GET", "API.EXAMPLE:443", token)
				require.Equal(t, 200, w.Code)
				require.Contains(t, w.Body.String(), "fixture-password")
			}
			w := serve("GET", "api.example", token)
			require.Equal(t, 429, w.Code)
			require.NotContains(t, w.Body.String(), "fixture-password")
			before := repo.calls
			mr.Close()
			require.Equal(t, 503, serve("GET", "api.example", token).Code)
			require.Equal(t, before, repo.calls)
		})
	}
}

func (f *clashDownloadStore) ReadMetadataSnapshot(ctx context.Context, a service.SubscriptionActor, fn func(service.ClashSubscriptionTx) error) error {
	return f.WithLockedOwner(ctx, a, fn)
}
