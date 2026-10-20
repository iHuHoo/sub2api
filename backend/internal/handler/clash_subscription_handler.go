package handler

import (
	"context"
	"errors"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ClashSubscriptionHandler struct {
	s     *service.ClashSubscriptionService
	redis *redis.Client
}

func NewClashSubscriptionHandler(s *service.ClashSubscriptionService, r *redis.Client) *ClashSubscriptionHandler {
	return &ClashSubscriptionHandler{s: s, redis: r}
}
func clashHeaders(c *gin.Context) {
	for name := range c.Writer.Header() {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "access-control-allow-") || lower == "access-control-expose-headers" {
			c.Writer.Header().Del(name)
		}
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
}
func clashHost(raw string) string {
	u, e := url.Parse("https://" + raw)
	if e != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if u.Port() != "" && u.Port() != "443" {
		host += ":" + u.Port()
	}
	return host
}
func (h *ClashSubscriptionHandler) Download(c *gin.Context) {
	clashHeaders(c)
	base, _ := url.Parse(h.s.PublicBaseURL())
	if !h.s.Enabled() || base == nil || clashHost(c.Request.Host) == "" || clashHost(c.Request.Host) != clashHost(base.Host) {
		c.Status(404)
		return
	}
	if c.Request.URL.Path != "/api/v1/clash/subscription" || c.Request.URL.RawPath != "" {
		c.Status(404)
		return
	}
	if c.Request.Method != "GET" {
		c.Header("Allow", "GET")
		c.Status(405)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	allow, err := middleware.ClashSubscriptionAllow(ctx, h.redis, "ip:"+c.ClientIP(), 60)
	if err != nil {
		c.Status(503)
		return
	}
	if !allow {
		c.Status(429)
		return
	}
	q, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q["token"]) != 1 || len(q.Get("token")) != 43 {
		c.Status(404)
		return
	}
	id, set, err := h.s.Snapshot(ctx, q.Get("token"))
	if err != nil {
		if errors.Is(err, service.ErrClashNotFound) || errors.Is(err, service.ErrClashDisabled) {
			c.Status(404)
		} else {
			c.Status(503)
		}
		return
	}
	allow, err = middleware.ClashSubscriptionAllow(ctx, h.redis, "id:"+strconv.FormatInt(id, 10), 10)
	if err != nil {
		c.Status(503)
		return
	}
	if !allow {
		c.Status(429)
		return
	}
	data, err := service.RenderClashConfig(set)
	if err != nil {
		c.Status(503)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="aoxtoken.yaml"`)
	c.Header("profile-update-interval", "1")
	c.Data(200, "application/yaml; charset=utf-8", data)
}
func (h *ClashSubscriptionHandler) Manage(c *gin.Context) {
	clashHeaders(c)
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	role, hasRole := middleware.GetUserRoleFromContext(c)
	if !ok || !hasRole || subject.UserID <= 0 {
		c.Status(401)
		return
	}
	a := service.SubscriptionActor{UserID: subject.UserID, Role: role}
	ctx := c.Request.Context()
	if c.Request.Method == "GET" {
		m, e := h.s.GetMetadata(ctx, a)
		if e != nil {
			clashError(c, e)
			return
		}
		response.Success(c, m)
		return
	}
	var input struct {
		DurationDays    int   `json:"duration_days"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "invalid subscription request")
		return
	}
	if c.Request.Method == "DELETE" {
		if e := h.s.Revoke(ctx, a, input.ExpectedVersion); e != nil {
			clashError(c, e)
			return
		}
		response.Success(c, gin.H{"revoked": true})
		return
	}
	var g service.SubscriptionGrant
	var err error
	if strings.HasSuffix(c.FullPath(), "/reset") {
		g, err = h.s.Reset(ctx, a, input.ExpectedVersion, input.DurationDays)
	} else {
		g, err = h.s.Create(ctx, a, input.DurationDays)
	}
	if err != nil {
		clashError(c, err)
		return
	}
	response.Success(c, g)
}
func clashError(c *gin.Context, err error) {
	e := infraerrors.FromError(err)
	if e.Code < 400 || e.Code >= 500 {
		c.JSON(503, response.Response{Code: 503, Message: "subscription temporarily unavailable"})
		return
	}
	c.JSON(int(e.Code), response.Response{Code: int(e.Code), Reason: e.Reason, Message: e.Message})
}
