package admin

import "github.com/Wei-Shaw/sub2api/internal/service"

// ParseSupplierCodexSessions reuses the import parser without admin account lookup or mutation.
func ParseSupplierCodexSessions(content string, contents []string) ([]service.SupplierAccountCreate, error) {
	entries, err := parseCodexSessionImportEntries(CodexSessionImportRequest{Content: content, Contents: contents})
	if err != nil || len(entries) == 0 || len(entries) > 100 {
		return nil, service.ErrSupplierInvalid
	}
	result := make([]service.SupplierAccountCreate, 0, len(entries))
	for _, entry := range entries {
		item, err := normalizeCodexImportEntry(entry)
		if err != nil || item.IsAgentIdentity {
			return nil, service.ErrSupplierInvalid
		}
		credentials := make(map[string]any)
		for _, key := range []string{"access_token", "refresh_token", "id_token", "expires_at", "email", "chatgpt_account_id", "chatgpt_user_id", "organization_id", "client_id", "plan_type"} {
			if value, ok := item.Credentials[key]; ok {
				credentials[key] = value
			}
		}
		result = append(result, service.SupplierAccountCreate{Name: item.Name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: credentials})
	}
	return result, nil
}
