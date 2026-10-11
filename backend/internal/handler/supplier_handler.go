package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SupplierHandler struct{ service *service.SupplierService }

func NewSupplierHandler(s *service.SupplierService) *SupplierHandler {
	return &SupplierHandler{service: s}
}
func bindSupplierJSON(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 65536)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		supplierError(c, service.ErrSupplierInvalid)
		return false
	}
	if dec.Decode(new(any)) != io.EOF {
		supplierError(c, service.ErrSupplierInvalid)
		return false
	}
	return true
}
func supplierError(c *gin.Context, err error) {
	for _, safe := range []error{service.ErrSupplierUnavailable, service.ErrSupplierInvalid, service.ErrSupplierReadOnly, service.ErrSupplierProxyInUse} {
		if errors.Is(err, safe) {
			response.ErrorFrom(c, safe)
			return
		}
	}
	response.InternalError(c, "Supplier operation failed")
}
func supplierSubject(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Authentication required")
		return 0, false
	}
	middleware.SetAuditExtra(c, map[string]any{"supplier_user_id": subject.UserID})
	return subject.UserID, true
}
func supplierID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		supplierError(c, service.ErrSupplierUnavailable)
		return 0, false
	}
	middleware.SetAuditExtra(c, map[string]any{"resource_id": id})
	return id, true
}
func supplierPagination(c *gin.Context) pagination.PaginationParams {
	page, size := response.ParsePagination(c)
	if page > 1000000 {
		page = 1000000
	}
	return pagination.PaginationParams{Page: page, PageSize: size}
}
func (h *SupplierHandler) ListAccounts(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	p := supplierPagination(c)
	rows, total, err := h.service.ListAccounts(c.Request.Context(), owner, p)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Paginated(c, rows, total, p.Page, p.PageSize)
}
func (h *SupplierHandler) GetAccount(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	row, err := h.service.GetAccount(c.Request.Context(), owner, id)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, row)
}
func (h *SupplierHandler) CreateAccount(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	var in service.SupplierAccountCreate
	if !bindSupplierJSON(c, &in) {
		return
	}
	row, err := h.service.CreateAccount(c.Request.Context(), owner, in)
	if err != nil {
		supplierError(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"resource_id": row.ID})
	response.Created(c, row)
}
func (h *SupplierHandler) UpdateAccount(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	var in service.SupplierAccountUpdate
	if !bindSupplierJSON(c, &in) {
		return
	}
	row, err := h.service.UpdateAccount(c.Request.Context(), owner, id, in)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, row)
}
func (h *SupplierHandler) PauseAccount(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	var in struct {
		Paused *bool `json:"paused"`
	}
	if !bindSupplierJSON(c, &in) {
		return
	}
	if in.Paused == nil {
		supplierError(c, service.ErrSupplierInvalid)
		return
	}
	row, err := h.service.PauseAccount(c.Request.Context(), owner, id, *in.Paused)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, row)
}
func (h *SupplierHandler) ListProxies(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	p := supplierPagination(c)
	rows, total, err := h.service.ListProxies(c.Request.Context(), owner, p)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Paginated(c, rows, total, p.Page, p.PageSize)
}
func (h *SupplierHandler) GetProxy(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	row, err := h.service.GetProxy(c.Request.Context(), owner, id)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, row)
}
func (h *SupplierHandler) CreateProxy(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	var in service.SupplierProxyInput
	if !bindSupplierJSON(c, &in) {
		return
	}
	row, err := h.service.CreateProxy(c.Request.Context(), owner, in)
	if err != nil {
		supplierError(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"resource_id": row.ID})
	response.Created(c, row)
}
func (h *SupplierHandler) UpdateProxy(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	var in service.SupplierProxyInput
	if !bindSupplierJSON(c, &in) {
		return
	}
	row, err := h.service.UpdateProxy(c.Request.Context(), owner, id, in)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, row)
}
func (h *SupplierHandler) DeleteProxy(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	if err := h.service.DeleteProxy(c.Request.Context(), owner, id); err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}
func (h *SupplierHandler) TestProxy(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	row, err := h.service.TestProxy(c.Request.Context(), owner, id)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, row)
}
func (h *SupplierHandler) Usage(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	today := time.Now().UTC()
	end := time.Date(today.Year(), today.Month(), today.Day()+1, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -30)
	if raw := c.Query("start_date"); raw != "" {
		v, err := time.Parse("2006-01-02", raw)
		if err != nil {
			supplierError(c, service.ErrSupplierInvalid)
			return
		}
		start = v
	}
	if raw := c.Query("end_date"); raw != "" {
		v, err := time.Parse("2006-01-02", raw)
		if err != nil {
			supplierError(c, service.ErrSupplierInvalid)
			return
		}
		end = v.AddDate(0, 0, 1)
	}
	var id *int64
	if raw := c.Query("account_id"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			supplierError(c, service.ErrSupplierInvalid)
			return
		}
		id = &v
	}
	out, err := h.service.Usage(c.Request.Context(), owner, start, end, id)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, out)
}
