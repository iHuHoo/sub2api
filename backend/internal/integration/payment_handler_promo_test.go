package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var paymentHandlerPromoDBID atomic.Uint64

func TestCreateOrderPassesSubscriptionPromoCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := paymentHandlerPromoDBID.Add(1)
	db, err := sql.Open("sqlite", fmt.Sprintf("file:payment_handler_promo_%d?mode=memory&cache=shared&_fk=1", id))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	settings := repository.NewSettingRepository(client)
	require.NoError(t, settings.Set(context.Background(), service.SettingPaymentEnabled, "true"))
	configService := service.NewPaymentConfigService(client, settings, nil)
	paymentService := service.NewPaymentService(client, payment.NewRegistry(), nil, nil, nil, configService, nil, nil, nil, nil)
	paymentHandler := handler.NewPaymentHandler(paymentService, configService)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/payment/orders", bytes.NewBufferString(`{
		"amount":10,"payment_type":"ldc","order_type":"balance","promo_code":"SAVE20"
	}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 7})

	paymentHandler.CreateOrder(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var response struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "PROMO_CODE_SUBSCRIPTION_ONLY", response.Reason)
}
