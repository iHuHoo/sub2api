package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SupplierHandler struct {
	service      *service.SupplierService
	oauth        *service.SupplierOAuthService
	accountUsage supplierAccountUsageReader
	accountTest  supplierAccountTester
}

type supplierAccountUsageReader interface {
	GetUsage(context.Context, int64, ...bool) (*service.UsageInfo, error)
	GetPassiveUsage(context.Context, int64) (*service.UsageInfo, error)
}

type supplierAccountTester interface {
	SupplierAvailableModels(context.Context, int64, int64) ([]service.AccountTestModel, error)
	TestSupplierAccountConnection(*gin.Context, int64, int64, string) error
}

type supplierUsageProgress struct {
	Utilization      float64    `json:"utilization"`
	ResetsAt         *time.Time `json:"resets_at"`
	RemainingSeconds int        `json:"remaining_seconds"`
}

type supplierAntigravityQuota struct {
	Utilization int    `json:"utilization"`
	ResetTime   string `json:"reset_time"`
}

type supplierAccountUsage struct {
	Source             string                               `json:"source,omitempty"`
	UpdatedAt          *time.Time                           `json:"updated_at"`
	FiveHour           *supplierUsageProgress               `json:"five_hour"`
	SevenDay           *supplierUsageProgress               `json:"seven_day,omitempty"`
	SevenDaySonnet     *supplierUsageProgress               `json:"seven_day_sonnet,omitempty"`
	SevenDayFable      *supplierUsageProgress               `json:"seven_day_fable,omitempty"`
	ThirtyDay          *supplierUsageProgress               `json:"thirty_day,omitempty"`
	GeminiSharedDaily  *supplierUsageProgress               `json:"gemini_shared_daily,omitempty"`
	GeminiProDaily     *supplierUsageProgress               `json:"gemini_pro_daily,omitempty"`
	GeminiFlashDaily   *supplierUsageProgress               `json:"gemini_flash_daily,omitempty"`
	GeminiSharedMinute *supplierUsageProgress               `json:"gemini_shared_minute,omitempty"`
	GeminiProMinute    *supplierUsageProgress               `json:"gemini_pro_minute,omitempty"`
	GeminiFlashMinute  *supplierUsageProgress               `json:"gemini_flash_minute,omitempty"`
	AntigravityQuota   map[string]*supplierAntigravityQuota `json:"antigravity_quota,omitempty"`
	ErrorCode          string                               `json:"error_code,omitempty"`
}

func supplierUsageProgressView(progress *service.UsageProgress) *supplierUsageProgress {
	if progress == nil {
		return nil
	}
	return &supplierUsageProgress{
		Utilization:      progress.Utilization,
		ResetsAt:         progress.ResetsAt,
		RemainingSeconds: progress.RemainingSeconds,
	}
}

func supplierUsageErrorCode(code string) string {
	switch code {
	case "", "quota_unknown":
		return ""
	case "rate_limited", "unauthenticated", "forbidden", "network_error", "unavailable":
		return code
	default:
		return "unavailable"
	}
}

func supplierAccountUsageView(usage *service.UsageInfo) supplierAccountUsage {
	if usage == nil {
		return supplierAccountUsage{ErrorCode: "unavailable"}
	}
	out := supplierAccountUsage{
		Source:             usage.Source,
		UpdatedAt:          usage.UpdatedAt,
		FiveHour:           supplierUsageProgressView(usage.FiveHour),
		SevenDay:           supplierUsageProgressView(usage.SevenDay),
		SevenDaySonnet:     supplierUsageProgressView(usage.SevenDaySonnet),
		SevenDayFable:      supplierUsageProgressView(usage.SevenDayFable),
		ThirtyDay:          supplierUsageProgressView(usage.ThirtyDay),
		GeminiSharedDaily:  supplierUsageProgressView(usage.GeminiSharedDaily),
		GeminiProDaily:     supplierUsageProgressView(usage.GeminiProDaily),
		GeminiFlashDaily:   supplierUsageProgressView(usage.GeminiFlashDaily),
		GeminiSharedMinute: supplierUsageProgressView(usage.GeminiSharedMinute),
		GeminiProMinute:    supplierUsageProgressView(usage.GeminiProMinute),
		GeminiFlashMinute:  supplierUsageProgressView(usage.GeminiFlashMinute),
		ErrorCode:          supplierUsageErrorCode(usage.ErrorCode),
	}
	if len(usage.AntigravityQuota) > 0 {
		out.AntigravityQuota = make(map[string]*supplierAntigravityQuota, len(usage.AntigravityQuota))
		for model, quota := range usage.AntigravityQuota {
			if quota != nil {
				out.AntigravityQuota[model] = &supplierAntigravityQuota{Utilization: quota.Utilization, ResetTime: quota.ResetTime}
			}
		}
	}
	return out
}

func NewSupplierHandlerWithOAuth(s *service.SupplierService, o *service.OpenAIOAuthService, c *service.OAuthService, g *service.GeminiOAuthService, a *service.AntigravityOAuthService, x *service.GrokOAuthService, accountTest *service.AccountTestService) *SupplierHandler {
	return &SupplierHandler{service: s, oauth: service.NewSupplierOAuthService(s, o, c, g, a, x), accountTest: accountTest}
}

func (h *SupplierHandler) OAuth(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	if h.oauth == nil {
		supplierError(c, service.ErrSupplierUnavailable)
		return
	}
	var in service.SupplierOAuthInput
	if !bindSupplierJSON(c, &in) {
		return
	}
	out, err := h.oauth.Execute(c.Request.Context(), owner, c.Param("platform"), c.Param("action"), in)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, out)
}
func (h *SupplierHandler) OAuthCapabilities(c *gin.Context) {
	if _, ok := supplierSubject(c); !ok {
		return
	}
	if h.oauth == nil {
		supplierError(c, service.ErrSupplierUnavailable)
		return
	}
	out := h.oauth.Capabilities(c.Param("platform"))
	if out == nil {
		supplierError(c, service.ErrSupplierInvalid)
		return
	}
	response.Success(c, out)
}

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
	for _, safe := range []error{service.ErrSupplierUnavailable, service.ErrSupplierInvalid, service.ErrSupplierReadOnly, service.ErrSupplierProxyInUse, service.ErrSupplierProxyReadOnly} {
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

func (h *SupplierHandler) GetAccountModels(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	if _, err := h.service.GetAccount(c.Request.Context(), owner, id); err != nil {
		supplierError(c, err)
		return
	}
	if h.accountTest == nil {
		supplierError(c, service.ErrSupplierUnavailable)
		return
	}
	models, err := h.accountTest.SupplierAvailableModels(c.Request.Context(), owner, id)
	if err != nil {
		supplierError(c, err)
		return
	}
	response.Success(c, models)
}

func (h *SupplierHandler) TestAccount(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	if _, err := h.service.GetAccount(c.Request.Context(), owner, id); err != nil {
		supplierError(c, err)
		return
	}
	var in struct {
		ModelID string `json:"model_id"`
	}
	if !bindSupplierJSON(c, &in) {
		return
	}
	if h.accountTest == nil {
		supplierError(c, service.ErrSupplierUnavailable)
		return
	}
	if err := h.accountTest.TestSupplierAccountConnection(c, owner, id, in.ModelID); errors.Is(err, service.ErrSupplierUnavailable) {
		supplierError(c, err)
	} else if err != nil {
		slog.Warn("supplier account test failed", "account_id", id, "supplier_user_id", owner)
	}
}

func (h *SupplierHandler) GetAccountUsage(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	id, ok := supplierID(c)
	if !ok {
		return
	}
	account, err := h.service.GetAccount(c.Request.Context(), owner, id)
	if err != nil {
		supplierError(c, err)
		return
	}
	if h.accountUsage == nil {
		supplierError(c, service.ErrSupplierUnavailable)
		return
	}
	var usage *service.UsageInfo
	if account.Platform == service.PlatformAnthropic && (account.Type == service.AccountTypeOAuth || account.Type == service.AccountTypeSetupToken) {
		usage, err = h.accountUsage.GetPassiveUsage(c.Request.Context(), id)
	} else {
		usage, err = h.accountUsage.GetUsage(c.Request.Context(), id)
	}
	if err != nil {
		slog.WarnContext(c.Request.Context(), "supplier account usage unavailable", "account_id", id, "error", err)
		response.Success(c, supplierAccountUsage{ErrorCode: "unavailable"})
		return
	}
	response.Success(c, supplierAccountUsageView(usage))
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
