//go:build unit

package admin

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyAssignmentRequiresExpectedField(t *testing.T) {
	h := NewProxyHandler(&stubAdminService{})
	r := gin.New()
	r.PUT("/:id/assignment", h.AssignSupplier)
	for _, body := range []string{`{}`, `{"supplier_user_id":null}`, `{"expected_supplier_user_id":null}`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/1/assignment", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, 400, w.Code)
	}
}

type assignmentAdminStub struct {
	service.AdminService
	expected, target *int64
	observed         *int64
	called           bool
	err              error
}

func (s *assignmentAdminStub) AssignProxySupplier(_ context.Context, id int64, expected, target *int64) (*service.Proxy, error) {
	s.called = true
	s.expected, s.target = expected, target
	if s.err != nil {
		return &service.Proxy{ID: id, AssignedSupplierUserID: s.observed}, s.err
	}
	return &service.Proxy{ID: id, AssignedSupplierUserID: target, AssignmentValid: true}, s.err
}
func TestProxyAssignmentExplicitNullAndConflict(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		err    error
	}{
		{`{"expected_supplier_user_id":null,"supplier_user_id":42}`, 200, nil},
		{`{"expected_supplier_user_id":42,"supplier_user_id":null}`, 200, nil},
		{`{"expected_supplier_user_id":null,"supplier_user_id":42}`, 409, service.ErrProxyAssignmentConflict},
	} {
		s := &assignmentAdminStub{err: tc.err}
		h := NewProxyHandler(s)
		r := gin.New()
		r.PUT("/:id/assignment", h.AssignSupplier)
		w := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/1/assignment", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, tc.status, w.Code)
		require.True(t, s.called)
		if strings.Contains(tc.body, `"supplier_user_id":null`) {
			require.Nil(t, s.target)
			require.EqualValues(t, 42, *s.expected)
		} else {
			require.Nil(t, s.expected)
			require.EqualValues(t, 42, *s.target)
		}
	}
}
func TestProxyAssignmentAuditDistinguishesExpectedFromObserved(t *testing.T) {
	actual := int64(2)
	s := &assignmentAdminStub{err: service.ErrProxyAssignmentConflict, observed: &actual}
	h := NewProxyHandler(s)
	r := gin.New()
	var extra map[string]any
	r.Use(func(c *gin.Context) { c.Next(); v, _ := c.Get("audit_extra"); extra, _ = v.(map[string]any) })
	r.PUT("/:id/assignment", h.AssignSupplier)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/1/assignment", strings.NewReader(`{"expected_supplier_user_id":1,"supplier_user_id":3}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, 409, w.Code)
	require.EqualValues(t, 1, extra["expected_supplier_user_id"])
	require.EqualValues(t, 3, extra["requested_supplier_user_id"])
	require.EqualValues(t, 2, extra["old_supplier_user_id"])
	require.Equal(t, "failed", extra["result"])
}
