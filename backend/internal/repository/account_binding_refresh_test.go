package repository

import (
	"context"
	"encoding/json"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestProductionCacheAuthoritativeBindings(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, change := range []string{"unbind-revoke", "unbind-reassign", "direct-to-unavailable", "deleted"} {
			t.Run(fmt.Sprintf("full=%v/%s", full, change), func(t *testing.T) {
				db, m, err := sqlmock.New()
				require.NoError(t, err)
				defer db.Close()
				c := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
				repo := newAccountRepositoryWithSQL(c, db, nil)
				owner, proxy := int64(9), int64(8)
				old := service.Account{ID: 1, Status: service.StatusActive, Schedulable: true, SupplierUserID: &owner, ProxyID: &proxy, Proxy: &service.Proxy{ID: proxy, Status: service.StatusActive, AssignedSupplierUserID: &owner, AssignmentValid: true}}
				if change == "direct-to-unavailable" {
					old.ProxyID = nil
					old.Proxy = nil
				}
				decode := func(a service.Account) *service.Account {
					meta, payload, e := marshalSchedulerCacheAccount(a)
					require.NoError(t, e)
					if full {
						payload = meta
					}
					var out service.Account
					require.NoError(t, json.Unmarshal(payload, &out))
					return &out
				}
				// Both formats use the real producer, and every read gets the retained old payload.
				cached := func() *assignmentBoundaryCache {
					return &assignmentBoundaryCache{snapshot: []*service.Account{decode(old), decode(service.Account{ID: 2, Status: service.StatusActive, Schedulable: true})}}
				}
				expect := func(list bool) {
					rows := sqlmock.NewRows([]string{"id", "proxy_id", "supplier_user_id"})
					if change != "deleted" {
						var binding any
						if change == "direct-to-unavailable" {
							binding = proxy
						}
						rows.AddRow(1, binding, owner)
					}
					if list {
						rows.AddRow(2, nil, nil)
					}
					m.ExpectQuery(`SELECT .* FROM "accounts"`).WillReturnRows(rows)
					if change == "direct-to-unavailable" {
						m.ExpectQuery(`SELECT .* FROM "proxies"`).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "assigned_supplier_user_id"}).AddRow(proxy, "disabled", owner))
						m.ExpectQuery(`SELECT .* FROM "users"`).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "role"}).AddRow(owner, "active", "supplier"))
					}
				}
				expect(true)
				svc := service.NewSchedulerSnapshotService(cached(), nil, repo, nil, nil)
				got, _, err := svc.ListSchedulableAccounts(context.Background(), nil, service.PlatformOpenAI, false)
				require.NoError(t, err)
				if change == "direct-to-unavailable" || change == "deleted" {
					require.Len(t, got, 1)
					require.Equal(t, int64(2), got[0].ID)
				} else {
					require.Len(t, got, 2)
					require.Nil(t, got[0].ProxyID)
					require.Nil(t, got[0].Proxy)
				}
				expect(false)
				svc = service.NewSchedulerSnapshotService(cached(), nil, repo, nil, nil)
				a, err := svc.GetAccount(context.Background(), 1)
				if change == "deleted" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					if change == "direct-to-unavailable" {
						require.False(t, a.IsSchedulable())
					} else {
						require.Nil(t, a.ProxyID)
						require.Nil(t, a.Proxy)
					}
				}
				require.NoError(t, m.ExpectationsWereMet())
			})
		}
	}
}
