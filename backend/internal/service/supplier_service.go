package service

import (
	"context"
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

var (
	ErrSupplierUnavailable = infraerrors.NotFound("SUPPLIER_RESOURCE_UNAVAILABLE", "Resource unavailable")
	ErrSupplierInvalid     = infraerrors.BadRequest("SUPPLIER_INVALID_INPUT", "Invalid supplier input")
	ErrSupplierReadOnly    = infraerrors.Conflict("SUPPLIER_ACCOUNT_READ_ONLY", "Derived account is read-only")
	ErrSupplierProxyInUse  = infraerrors.Conflict("SUPPLIER_PROXY_IN_USE", "Proxy is in use")
)

type SupplierAccountCreate struct {
	Name        string         `json:"name"`
	Notes       *string        `json:"notes"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
	ProxyID     *int64         `json:"proxy_id"`
	ExpiresAt   *int64         `json:"expires_at"`
}

type SupplierAccountUpdate struct {
	Name        *string        `json:"name"`
	Notes       *string        `json:"notes"`
	Credentials map[string]any `json:"credentials"`
	ProxyID     *int64         `json:"proxy_id"`
	ExpiresAt   *int64         `json:"expires_at"`
}

type SupplierAccount struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Notes          *string    `json:"notes"`
	Platform       string     `json:"platform"`
	Type           string     `json:"type"`
	Status         string     `json:"status"`
	SupplierPaused bool       `json:"supplier_paused"`
	ReadOnly       bool       `json:"read_only"`
	ProxyID        *int64     `json:"proxy_id"`
	ExpiresAt      *time.Time `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type SupplierProxyInput struct {
	Name     *string `json:"name"`
	Protocol *string `json:"protocol"`
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	Status   *string `json:"status"`
}

type SupplierProxy struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Protocol    string    `json:"protocol"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Username    string    `json:"username"`
	HasPassword bool      `json:"has_password"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SupplierTokens struct {
	Requests            int64 `json:"requests"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
}
type SupplierDailyUsage struct {
	Date string `json:"date"`
	SupplierTokens
}
type SupplierUsage struct {
	Timezone string               `json:"timezone"`
	Summary  SupplierTokens       `json:"summary"`
	Daily    []SupplierDailyUsage `json:"daily"`
}
type SupplierProxyTest struct {
	Reachable bool  `json:"reachable"`
	LatencyMs int64 `json:"latency_ms"`
}

type SupplierRepository interface {
	ListAccounts(context.Context, int64, pagination.PaginationParams) ([]Account, int64, error)
	GetAccount(context.Context, int64, int64) (*Account, error)
	CreateAccount(context.Context, int64, *Account) error
	UpdateAccount(context.Context, int64, int64, SupplierAccountUpdate) (*Account, error)
	PauseAccount(context.Context, int64, int64, bool) (*Account, error)
	ListProxies(context.Context, int64, pagination.PaginationParams) ([]Proxy, int64, error)
	GetProxy(context.Context, int64, int64) (*Proxy, error)
	CreateProxy(context.Context, int64, *Proxy) error
	UpdateProxy(context.Context, int64, int64, SupplierProxyInput) (*Proxy, error)
	DeleteProxy(context.Context, int64, int64) error
	Usage(context.Context, int64, time.Time, time.Time, *int64) (*SupplierUsage, error)
}

type SupplierService struct{ repo SupplierRepository }

func NewSupplierService(repo SupplierRepository) *SupplierService {
	return &SupplierService{repo: repo}
}

func supplierAccountView(a *Account, owner int64) SupplierAccount {
	out := SupplierAccount{ID: a.ID, Name: a.Name, Notes: a.SupplierNotes, Platform: a.Platform, Type: a.Type, Status: a.Status, SupplierPaused: a.SupplierPaused, ReadOnly: a.ParentAccountID != nil, ExpiresAt: a.ExpiresAt, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if a.Proxy != nil && a.Proxy.SupplierUserID != nil && *a.Proxy.SupplierUserID == owner {
		out.ProxyID = a.ProxyID
	}
	return out
}
func supplierProxyView(p *Proxy) SupplierProxy {
	return SupplierProxy{ID: p.ID, Name: p.Name, Protocol: p.Protocol, Host: p.Host, Port: p.Port, Username: p.Username, HasPassword: p.Password != "", Status: p.Status, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}
func (s *SupplierService) ListAccounts(ctx context.Context, owner int64, p pagination.PaginationParams) ([]SupplierAccount, int64, error) {
	rows, total, err := s.repo.ListAccounts(ctx, owner, p)
	if err != nil {
		return nil, 0, err
	}
	out := make([]SupplierAccount, 0, len(rows))
	for i := range rows {
		out = append(out, supplierAccountView(&rows[i], owner))
	}
	return out, total, nil
}
func (s *SupplierService) GetAccount(ctx context.Context, owner, id int64) (*SupplierAccount, error) {
	a, err := s.repo.GetAccount(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	out := supplierAccountView(a, owner)
	return &out, nil
}
func (s *SupplierService) CreateAccount(ctx context.Context, owner int64, in SupplierAccountCreate) (*SupplierAccount, error) {
	if !validSupplierName(in.Name) || !validSupplierNotes(in.Notes) || !validSupplierExpiry(in.ExpiresAt) {
		return nil, ErrSupplierInvalid
	}
	if err := validateSupplierCredentials(in.Platform, in.Type, in.Credentials, true); err != nil {
		return nil, err
	}
	input := &CreateAccountInput{Name: strings.TrimSpace(in.Name), Platform: in.Platform, Type: in.Type, Credentials: in.Credentials, ProxyID: in.ProxyID, ExpiresAt: in.ExpiresAt, SupplierUserID: &owner, SkipDefaultGroupBind: true, Concurrency: 1}
	a, err := buildAccountForCreate(input, nil)
	if err != nil {
		return nil, ErrSupplierInvalid
	}
	a.SupplierNotes = normalizeAccountNotes(in.Notes)
	if err = s.repo.CreateAccount(ctx, owner, a); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, owner, a.ID)
}
func (s *SupplierService) UpdateAccount(ctx context.Context, owner, id int64, in SupplierAccountUpdate) (*SupplierAccount, error) {
	a, err := s.repo.GetAccount(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if a.ParentAccountID != nil {
		return nil, ErrSupplierReadOnly
	}
	if in.Name != nil && !validSupplierName(*in.Name) || !validSupplierNotes(in.Notes) || !validSupplierExpiry(in.ExpiresAt) {
		return nil, ErrSupplierInvalid
	}
	if in.Credentials != nil {
		if err = validateSupplierCredentials(a.Platform, a.Type, in.Credentials, false); err != nil {
			return nil, err
		}
		merged := mergeMap(a.Credentials, in.Credentials)
		if err = validateSupplierAuthentication(a.Type, merged); err != nil {
			return nil, err
		}

	}
	a, err = s.repo.UpdateAccount(ctx, owner, id, in)
	if err != nil {
		return nil, err
	}
	out := supplierAccountView(a, owner)
	return &out, nil
}
func (s *SupplierService) PauseAccount(ctx context.Context, owner, id int64, paused bool) (*SupplierAccount, error) {
	a, err := s.repo.PauseAccount(ctx, owner, id, paused)
	if err != nil {
		return nil, err
	}
	out := supplierAccountView(a, owner)
	return &out, nil
}
func (s *SupplierService) ListProxies(ctx context.Context, owner int64, p pagination.PaginationParams) ([]SupplierProxy, int64, error) {
	rows, total, err := s.repo.ListProxies(ctx, owner, p)
	if err != nil {
		return nil, 0, err
	}
	out := make([]SupplierProxy, 0, len(rows))
	for i := range rows {
		out = append(out, supplierProxyView(&rows[i]))
	}
	return out, total, nil
}
func (s *SupplierService) GetProxy(ctx context.Context, owner, id int64) (*SupplierProxy, error) {
	p, err := s.repo.GetProxy(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	out := supplierProxyView(p)
	return &out, nil
}
func applySupplierProxyInput(p *Proxy, in SupplierProxyInput) {
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Protocol != nil {
		p.Protocol = *in.Protocol
	}
	if in.Host != nil {
		p.Host = *in.Host
	}
	if in.Port != nil {
		p.Port = *in.Port
	}
	if in.Username != nil {
		p.Username = *in.Username
	}
	if in.Password != nil {
		p.Password = *in.Password
	}
	if in.Status != nil {
		p.Status = *in.Status
	}
}
func (s *SupplierService) CreateProxy(ctx context.Context, owner int64, in SupplierProxyInput) (*SupplierProxy, error) {
	p := &Proxy{Status: StatusActive, SupplierUserID: &owner}
	applySupplierProxyInput(p, in)
	if err := validateSupplierProxy(p); err != nil {
		return nil, err
	}
	if err := s.repo.CreateProxy(ctx, owner, p); err != nil {
		return nil, err
	}
	out := supplierProxyView(p)
	return &out, nil
}
func (s *SupplierService) UpdateProxy(ctx context.Context, owner, id int64, in SupplierProxyInput) (*SupplierProxy, error) {
	p, err := s.repo.GetProxy(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	applySupplierProxyInput(p, in)
	if err = validateSupplierProxy(p); err != nil {
		return nil, err
	}
	p, err = s.repo.UpdateProxy(ctx, owner, id, in)
	if err != nil {
		return nil, err
	}
	out := supplierProxyView(p)
	return &out, nil
}
func (s *SupplierService) DeleteProxy(ctx context.Context, owner, id int64) error {
	return s.repo.DeleteProxy(ctx, owner, id)
}
func (s *SupplierService) TestProxy(ctx context.Context, owner, id int64) (*SupplierProxyTest, error) {
	p, err := s.repo.GetProxy(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if err = validateSupplierProxy(p); err != nil {
		return nil, ErrSupplierInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := safeDialContext(ctx, "tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
	out := &SupplierProxyTest{Reachable: err == nil, LatencyMs: time.Since(start).Milliseconds()}
	if conn != nil {
		_ = conn.Close()
	}
	return out, nil
}
func (s *SupplierService) Usage(ctx context.Context, owner int64, start, end time.Time, id *int64) (*SupplierUsage, error) {
	if !end.After(start) || end.Sub(start) > 366*24*time.Hour || id != nil && *id <= 0 {
		return nil, ErrSupplierInvalid
	}
	return s.repo.Usage(ctx, owner, start, end, id)
}
func validSupplierName(s string) bool   { return strings.TrimSpace(s) != "" && len(s) <= 100 }
func validSupplierNotes(s *string) bool { return s == nil || len(*s) <= 2000 }
func validSupplierExpiry(v *int64) bool { return v == nil || *v >= 0 && *v <= 253402300799 }
func validateSupplierProxy(p *Proxy) error {
	ip := net.ParseIP(p.Host)
	if !validSupplierName(p.Name) || ip == nil || isPrivateIP(ip) || supplierReservedIP(ip) || !ip.IsGlobalUnicast() || p.Port < 1 || p.Port > 65535 || len(p.Username) > 255 || len(p.Password) > 255 {
		return ErrSupplierInvalid
	}
	switch p.Protocol {
	case "http", "https", "socks5", "socks5h":
	default:
		return ErrSupplierInvalid
	}
	if p.Status != StatusActive && p.Status != StatusDisabled {
		return ErrSupplierInvalid
	}
	return nil
}

var supplierReservedCIDRs = mustParseCIDRs([]string{
	"192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:2::/48", "2001:db8::/32", "2002::/16",
})

func supplierReservedIP(ip net.IP) bool {
	for _, cidr := range supplierReservedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

var supplierLocationPattern = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)
var supplierProjectPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9:._-]{0,127}$`)

func supplierCredentialKeys(platform, typ string) map[string]bool {
	keys := map[string]bool{}
	var allowed []string
	switch typ {
	case AccountTypeAPIKey:
		allowed = []string{"api_key", "base_url"}
	case AccountTypeUpstream:
		if platform == PlatformOpenAI || platform == PlatformAnthropic {
			allowed = []string{"api_key", "base_url"}
		}
	case AccountTypeSetupToken:
		if platform == PlatformAnthropic {
			allowed = []string{"access_token", "expires_at"}
		}
	case AccountTypeOAuth:
		switch platform {
		case PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok:
			allowed = []string{"access_token", "refresh_token", "id_token", "expires_at", "token_type", "scope", "email"}
		}
		if platform == PlatformOpenAI {
			allowed = append(allowed, "chatgpt_account_id", "chatgpt_user_id", "organization_id")
		}
		if platform == PlatformAnthropic {
			allowed = append(allowed, "claude_user_id", "anthropic_user_id")
		}
		if platform == PlatformGemini || platform == PlatformAntigravity {
			allowed = append(allowed, "project_id")
		}
	case AccountTypeServiceAccount:
		if platform == PlatformGemini || platform == PlatformAnthropic {
			allowed = []string{"service_account_json", "project_id", "location"}
		}
	case AccountTypeBedrock:
		if platform == PlatformAnthropic {
			allowed = []string{"auth_mode", "api_key", "aws_access_key_id", "aws_secret_access_key", "aws_session_token", "aws_region"}
		}
	}
	for _, k := range allowed {
		keys[k] = true
	}
	return keys
}

func supplierOfficialURLs(platform string) []string {
	switch platform {
	case PlatformAnthropic:
		return []string{"https://api.anthropic.com"}
	case PlatformOpenAI:
		return []string{"https://api.openai.com/v1"}
	case PlatformGemini:
		return []string{"https://generativelanguage.googleapis.com"}
	case PlatformGrok:
		return []string{"https://api.x.ai/v1"}
	case PlatformKimi:
		return []string{DefaultKimiPayGBaseURL, DefaultKimiCodingBaseURL}
	case PlatformZhipu:
		return []string{DefaultZhipuPayGBaseURL, DefaultZhipuCodingBaseURL}
	case PlatformDeepseek:
		return []string{DefaultDeepseekBaseURL}
	case PlatformMiniMax:
		return []string{DefaultMiniMaxBaseURL}
	case PlatformOpenCodeGo:
		return []string{DefaultOpenCodeGoBaseURL, DefaultOpenCodeZenBaseURL}
	case PlatformCommandCode:
		return []string{DefaultCommandCodeBaseURL}
	case PlatformCline:
		return []string{DefaultClineBaseURL}
	case PlatformTypeSafe:
		return []string{"https://api.typesafe.ai"}
	}
	return nil
}

func validateSupplierCredentials(platform, typ string, creds map[string]any, required bool) error {
	keys := supplierCredentialKeys(platform, typ)
	if len(keys) == 0 || platform == PlatformAntigravity && typ != AccountTypeOAuth {
		return ErrSupplierInvalid
	}
	if typ == AccountTypeAPIKey || typ == AccountTypeUpstream {
		if len(supplierOfficialURLs(platform)) == 0 {
			return ErrSupplierInvalid
		}
	}
	for k, v := range creds {
		if !keys[k] {
			return ErrSupplierInvalid
		}
		if k == "service_account_json" {
			raw, ok := v.(string)
			if !ok || len(raw) > 32768 {
				return ErrSupplierInvalid
			}
			normalized, err := validateSupplierServiceAccount(raw)
			if err != nil {
				return err
			}
			creds[k] = normalized
			continue
		}
		if k == "expires_at" {
			switch n := v.(type) {
			case string:
				if len(n) > 64 {
					return ErrSupplierInvalid
				}
			case float64:
				if n < 0 || n > 253402300799 {
					return ErrSupplierInvalid
				}
			case int64:
				if n < 0 || n > 253402300799 {
					return ErrSupplierInvalid
				}
			default:
				return ErrSupplierInvalid
			}
			continue
		}
		str, ok := v.(string)
		if !ok || len(str) > 16384 {
			return ErrSupplierInvalid
		}
		if strings.ContainsAny(str, "\r\n") {
			return ErrSupplierInvalid
		}
		if k == "base_url" {
			found := false
			for _, u := range supplierOfficialURLs(platform) {
				if str == u {
					found = true
				}
			}
			if !found {
				return ErrSupplierInvalid
			}
		}
		if k == "project_id" && !supplierProjectPattern.MatchString(str) || (k == "location" || k == "aws_region") && !supplierLocationPattern.MatchString(str) {
			return ErrSupplierInvalid
		}
	}
	if !required {
		return nil
	}
	return validateSupplierAuthentication(typ, creds)
}
func validateSupplierAuthentication(typ string, creds map[string]any) error {
	nonempty := func(k string) bool { s, _ := creds[k].(string); return strings.TrimSpace(s) != "" }
	switch typ {
	case AccountTypeAPIKey, AccountTypeUpstream:
		if typ == AccountTypeUpstream && !nonempty("base_url") {
			return ErrSupplierInvalid
		}
		if !nonempty("api_key") {
			return ErrSupplierInvalid
		}
	case AccountTypeOAuth, AccountTypeSetupToken:
		if !nonempty("access_token") {
			return ErrSupplierInvalid
		}
	case AccountTypeServiceAccount:
		if !nonempty("service_account_json") {
			return ErrSupplierInvalid
		}
	case AccountTypeBedrock:
		if !nonempty("aws_region") {
			return ErrSupplierInvalid
		}
		if creds["auth_mode"] == "apikey" {
			if !nonempty("api_key") {
				return ErrSupplierInvalid
			}
		} else if creds["auth_mode"] != "sigv4" || !nonempty("aws_access_key_id") || !nonempty("aws_secret_access_key") {
			return ErrSupplierInvalid
		}
	}
	return nil
}
func validateSupplierServiceAccount(raw string) (string, error) {
	var key vertexServiceAccountKey
	// Google downloads include these inert metadata fields; never retain arbitrary external credential config.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", ErrSupplierInvalid
	}
	allowed := map[string]bool{"type": true, "project_id": true, "private_key_id": true, "private_key": true, "client_email": true, "client_id": true, "auth_uri": true, "token_uri": true, "auth_provider_x509_cert_url": true, "client_x509_cert_url": true, "universe_domain": true}
	for k := range fields {
		if !allowed[k] {
			return "", ErrSupplierInvalid
		}
	}
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return "", ErrSupplierInvalid
	}
	if key.Type != "service_account" || key.TokenURI != "" && key.TokenURI != vertexDefaultTokenURL || !supplierProjectPattern.MatchString(key.ProjectID) || strings.TrimSpace(key.ClientEmail) == "" {
		return "", ErrSupplierInvalid
	}
	if _, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(key.PrivateKey)); err != nil {
		return "", ErrSupplierInvalid
	}
	key.TokenURI = vertexDefaultTokenURL
	normalized, err := json.Marshal(key)
	if err != nil {
		return "", ErrSupplierInvalid
	}
	return string(normalized), nil
}
