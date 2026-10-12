package admin

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strconv"
)

type ProxyDiagnosticsHandler struct {
	svc *service.ProxyDiagnosticsService
}

func NewProxyDiagnosticsHandler(svc *service.ProxyDiagnosticsService) *ProxyDiagnosticsHandler {
	return &ProxyDiagnosticsHandler{svc}
}
func (h *ProxyDiagnosticsHandler) Capabilities(c *gin.Context) {
	response.Success(c, gin.H{"enabled": h.svc.Enabled(), "schema_version": 1, "modes": []string{"quick", "sample"}})
}
func (h *ProxyDiagnosticsHandler) Check(c *gin.Context) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}
	d := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	start, startErr := d.Token()
	key, keyErr := d.Token()
	var mode string
	valueErr := d.Decode(&mode)
	end, endErr := d.Token()
	if startErr != nil || keyErr != nil || valueErr != nil || endErr != nil || start != json.Delim('{') || key != "mode" || end != json.Delim('}') || d.Decode(new(any)) != io.EOF || (mode != "quick" && mode != "sample") {
		response.BadRequest(c, "Body must contain only mode: quick or sample")
		return
	}
	result, e := h.svc.Check(c.Request.Context(), id, mode)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, result)
}
func (h *ProxyDiagnosticsHandler) Latest(c *gin.Context) {
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.BadRequest(c, "Invalid proxy ID")
		return
	}
	result, e := h.svc.Latest(c.Request.Context(), id)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, result)
}
