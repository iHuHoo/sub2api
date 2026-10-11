package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"strings"
	"sync"
	"time"
)

type SupplierOAuthInput struct {
	ProxyID      *int64 `json:"proxy_id"`
	SessionID    string `json:"session_id"`
	Code         string `json:"code"`
	State        string `json:"state"`
	RefreshToken string `json:"refresh_token"`
	RT           string `json:"rt"`
	ClientID     string `json:"client_id"`
	ProjectID    string `json:"project_id"`
	OAuthType    string `json:"oauth_type"`
	TierID       string `json:"tier_id"`
	SessionKey   string `json:"session_key"`
	SSOToken     string `json:"sso_token"`
	Email        string `json:"email"`
	Password     string `json:"password"`
}
type supplierOAuthSession struct {
	owner                           int64
	platform, internalID, oauthType string
	proxyID                         *int64
	expires                         time.Time
}
type SupplierOAuthService struct {
	supplier    *SupplierService
	openai      *OpenAIOAuthService
	claude      *OAuthService
	gemini      *GeminiOAuthService
	antigravity *AntigravityOAuthService
	grok        *GrokOAuthService
	mu          sync.Mutex
	sessions    map[string]supplierOAuthSession
}

func NewSupplierOAuthService(s *SupplierService, o *OpenAIOAuthService, c *OAuthService, g *GeminiOAuthService, a *AntigravityOAuthService, x *GrokOAuthService) *SupplierOAuthService {
	return &SupplierOAuthService{supplier: s, openai: o, claude: c, gemini: g, antigravity: a, grok: x, sessions: make(map[string]supplierOAuthSession)}
}
func (s *SupplierOAuthService) takeSession(owner int64, platform, id string) (supplierOAuthSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[id]
	if !ok || v.owner != owner || v.platform != platform || time.Now().After(v.expires) {
		return supplierOAuthSession{}, ErrSupplierInvalid
	}
	delete(s.sessions, id)
	return v, nil
}
func (s *SupplierOAuthService) proxyURL(ctx context.Context, owner int64, id *int64) (string, error) {
	if id == nil {
		return "", nil
	}
	if *id <= 0 {
		return "", ErrSupplierInvalid
	}
	p, err := s.supplier.repo.GetProxy(ctx, owner, *id)
	if err != nil {
		return "", err
	}
	if p == nil || p.SupplierUserID == nil || *p.SupplierUserID != owner {
		return "", ErrSupplierUnavailable
	}
	if err = validateSupplierProxy(p); err != nil {
		return "", err
	}
	return p.URL(), nil
}
func (s *SupplierOAuthService) Capabilities(platform string) any {
	if platform == PlatformGemini {
		return s.gemini.GetOAuthConfig()
	}
	if platform == PlatformGrok {
		return s.grok.GetCapabilities()
	}
	return nil
}
func (s *SupplierOAuthService) Execute(ctx context.Context, owner int64, platform, action string, in SupplierOAuthInput) (any, error) {
	if owner <= 0 {
		return nil, ErrSupplierInvalid
	}
	proxyURL, err := s.proxyURL(ctx, owner, in.ProxyID)
	if err != nil {
		return nil, err
	}
	if in.OAuthType == "" {
		in.OAuthType = "code_assist"
	}
	if platform == PlatformGemini && in.OAuthType != "code_assist" && in.OAuthType != "google_one" && in.OAuthType != "ai_studio" {
		return nil, ErrSupplierInvalid
	}
	if action == "generate-auth-url" || action == "generate-setup-token-url" {
		if action == "generate-setup-token-url" && platform != PlatformAnthropic {
			return nil, ErrSupplierInvalid
		}
		var url, id, state string
		switch platform {
		case PlatformOpenAI:
			r, e := s.openai.GenerateAuthURL(ctx, in.ProxyID, "", platform)
			err = e
			if e == nil {
				url, id = r.AuthURL, r.SessionID
			}
		case PlatformAnthropic:
			var r *GenerateAuthURLResult
			if action == "generate-setup-token-url" {
				r, err = s.claude.GenerateSetupTokenURL(ctx, in.ProxyID)
			} else {
				r, err = s.claude.GenerateAuthURL(ctx, in.ProxyID)
			}
			if err == nil {
				url, id = r.AuthURL, r.SessionID
			}
		case PlatformGemini:
			r, e := s.gemini.GenerateAuthURL(ctx, in.ProxyID, "", in.ProjectID, in.OAuthType, in.TierID)
			err = e
			if e == nil {
				url, id, state = r.AuthURL, r.SessionID, r.State
			}
		case PlatformAntigravity:
			r, e := s.antigravity.GenerateAuthURL(ctx, in.ProxyID)
			err = e
			if e == nil {
				url, id, state = r.AuthURL, r.SessionID, r.State
			}
		case PlatformGrok:
			r, e := s.grok.GenerateAuthURL(ctx, in.ProxyID, "")
			err = e
			if e == nil {
				url, id, state = r.AuthURL, r.SessionID, r.State
			}
		default:
			return nil, ErrSupplierInvalid
		}
		if err != nil {
			return nil, err
		}
		publicID, e := openai.GenerateSessionID()
		if e != nil {
			return nil, e
		}
		s.mu.Lock()
		for k, v := range s.sessions {
			if time.Now().After(v.expires) {
				delete(s.sessions, k)
			}
		}
		// Bound pending supplier sessions independently of the upstream session stores.
		if len(s.sessions) >= 10000 {
			s.mu.Unlock()
			return nil, ErrSupplierInvalid
		}
		s.sessions[publicID] = supplierOAuthSession{owner: owner, platform: platform, internalID: id, oauthType: in.OAuthType, proxyID: in.ProxyID, expires: time.Now().Add(15 * time.Minute)}
		s.mu.Unlock()
		return map[string]string{"auth_url": url, "session_id": publicID, "state": state}, nil
	}
	if action == "exchange-code" || action == "exchange-setup-token-code" {
		if strings.TrimSpace(in.Code) == "" {
			return nil, ErrSupplierInvalid
		}
		session, e := s.takeSession(owner, platform, in.SessionID)
		if e != nil {
			return nil, e
		}
		// Recheck the original proxy ownership even when the request omits proxy_id.
		if _, e = s.proxyURL(ctx, owner, session.proxyID); e != nil {
			return nil, e
		}
		if in.ProxyID != nil && (session.proxyID == nil || *in.ProxyID != *session.proxyID) {
			return nil, ErrSupplierInvalid
		}
		switch platform {
		case PlatformOpenAI:
			return s.openai.ExchangeCode(ctx, &OpenAIExchangeCodeInput{SessionID: session.internalID, Code: in.Code, State: in.State, ProxyID: session.proxyID})
		case PlatformAnthropic:
			return s.claude.ExchangeCode(ctx, &ExchangeCodeInput{SessionID: session.internalID, Code: in.Code, ProxyID: session.proxyID})
		case PlatformGemini:
			if session.oauthType != in.OAuthType {
				return nil, ErrSupplierInvalid
			}
			return s.gemini.ExchangeCode(ctx, &GeminiExchangeCodeInput{SessionID: session.internalID, Code: in.Code, State: in.State, ProxyID: session.proxyID, OAuthType: session.oauthType, TierID: in.TierID})
		case PlatformAntigravity:
			return s.antigravity.ExchangeCode(ctx, &AntigravityExchangeCodeInput{SessionID: session.internalID, Code: in.Code, State: in.State, ProxyID: session.proxyID})
		case PlatformGrok:
			return s.grok.ExchangeCode(ctx, &GrokExchangeCodeInput{SessionID: session.internalID, Code: in.Code, State: in.State, ProxyID: session.proxyID})
		}
	}
	if action == "refresh-token" || action == "validate-refresh-token" {
		token := strings.TrimSpace(in.RefreshToken)
		if token == "" {
			token = strings.TrimSpace(in.RT)
		}
		if token == "" {
			return nil, ErrSupplierInvalid
		}
		switch platform {
		case PlatformOpenAI:
			if strings.TrimSpace(in.ClientID) == "" {
				in.ClientID, _ = openai.OAuthClientConfigByPlatform(PlatformOpenAI)
			}
			return s.openai.RefreshTokenWithClientID(ctx, token, proxyURL, in.ClientID)
		case PlatformAnthropic:
			return s.claude.RefreshToken(ctx, token, proxyURL)
		case PlatformGemini:
			return s.gemini.RefreshToken(ctx, in.OAuthType, token, proxyURL)
		case PlatformAntigravity:
			return s.antigravity.ValidateRefreshToken(ctx, token, in.ProxyID)
		case PlatformGrok:
			return s.grok.RefreshToken(ctx, token, proxyURL, in.ClientID)
		}
	}
	if platform == PlatformAnthropic && (action == "cookie-auth" || action == "setup-token-cookie-auth") {
		if strings.TrimSpace(in.SessionKey) == "" {
			return nil, ErrSupplierInvalid
		}
		scope := "full"
		if action == "setup-token-cookie-auth" {
			scope = "inference"
		}
		return s.claude.CookieAuth(ctx, &CookieAuthInput{SessionKey: in.SessionKey, ProxyID: in.ProxyID, Scope: scope})
	}
	if platform == PlatformGrok && action == "validate-sso-token" {
		if strings.TrimSpace(in.SSOToken) == "" {
			return nil, ErrSupplierInvalid
		}
		return s.grok.ValidateSSOToken(ctx, in.SSOToken, in.ProxyID)
	}
	if platform == PlatformGrok && action == "authorize-password" {
		if strings.TrimSpace(in.Email) == "" || in.Password == "" {
			return nil, ErrSupplierInvalid
		}
		return s.grok.AuthorizePassword(ctx, in.Email, in.Password, in.ProxyID)
	}
	return nil, ErrSupplierInvalid
}
