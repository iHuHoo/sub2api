package handler

import (
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *SupplierHandler) ImportCodexSession(c *gin.Context) {
	owner, ok := supplierSubject(c)
	if !ok {
		return
	}
	var in struct {
		Content   string   `json:"content"`
		Contents  []string `json:"contents"`
		Name      string   `json:"name"`
		Notes     *string  `json:"notes"`
		ProxyID   *int64   `json:"proxy_id"`
		ExpiresAt *int64   `json:"expires_at"`
	}
	if !bindSupplierJSON(c, &in) {
		return
	}
	// Ownership is checked before parsing, and checked again by each account mutation.
	if in.ProxyID != nil {
		if _, err := h.service.GetProxy(c.Request.Context(), owner, *in.ProxyID); err != nil {
			supplierError(c, err)
			return
		}
	}
	rows, err := admin.ParseSupplierCodexSessions(in.Content, in.Contents)
	if err != nil {
		supplierError(c, err)
		return
	}
	out := admin.CodexSessionImportResult{Total: len(rows)}
	for i, row := range rows {
		if in.Name != "" {
			row.Name = in.Name
			if len(rows) > 1 {
				row.Name = fmt.Sprintf("%s-%d", in.Name, i+1)
			}
		}
		row.Notes = in.Notes
		row.ProxyID = in.ProxyID
		row.ExpiresAt = in.ExpiresAt
		account, err := h.service.CreateAccount(c.Request.Context(), owner, row)
		if err != nil {
			out.Failed++
			out.Errors = append(out.Errors, admin.CodexSessionImportMessage{Index: i + 1, Message: "Supplier account import failed"})
			continue
		}
		out.Created++
		out.Items = append(out.Items, admin.CodexSessionImportItem{Index: i + 1, Name: account.Name, Action: "created", AccountID: account.ID})
	}
	response.Success(c, out)
}
