//go:build unit

package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplierCredentialsWhitelist(t *testing.T) {
	for _, key := range []string{"model_mapping", "header_overrides", "protocol_rules", "token_uri", "base_url"} {
		creds := map[string]any{"api_key": "secret", key: "https://127.0.0.1"}
		require.Error(t, validateSupplierCredentials(PlatformOpenAI, AccountTypeAPIKey, creds, true), key)
	}
	creds := map[string]any{"api_key": "secret", "base_url": "https://api.openai.com/v1"}
	require.NoError(t, validateSupplierCredentials(PlatformOpenAI, AccountTypeAPIKey, creds, true))
	for _, u := range []string{"https://api.openai.com:444/v1", "https://u:p@api.openai.com/v1", "https://api.openai.com/v1/redirect", "https://api.openai.com/v1?url=x", "https://api.openai.com.evil/v1"} {
		creds["base_url"] = u
		require.Error(t, validateSupplierCredentials(PlatformOpenAI, AccountTypeAPIKey, creds, true), u)
	}
}

func TestSupplierProxyPublicLiteralOnly(t *testing.T) {
	for _, host := range []string{"localhost", "proxy.example.com", "127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "224.0.0.1", "192.0.2.1", "198.18.0.1", "240.0.0.1", "2001:db8::1", "64:ff9b::a00:1", "2002:7f00:1::1"} {
		require.Error(t, validateSupplierProxy(&Proxy{Name: "p", Protocol: "http", Host: host, Port: 8080, Status: StatusActive}), host)
	}
	require.NoError(t, validateSupplierProxy(&Proxy{Name: "p", Protocol: "socks5", Host: "8.8.8.8", Port: 1080, Status: StatusActive}))
}

func TestSupplierAccountResponseWhitelist(t *testing.T) {
	owner := int64(11)
	foreign := int64(22)
	proxyID := int64(100)
	adminNote := "admin-secret"
	note := "supplier-note"
	lastUsedAt := time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)
	a := &Account{ID: 1, Name: "a", SupplierUserID: &owner, Notes: &adminNote, SupplierNotes: &note, Credentials: map[string]any{"api_key": "secret"}, Extra: map[string]any{"customer": "private"}, ProxyID: &proxyID, Proxy: &Proxy{ID: proxyID, SupplierUserID: &foreign, Host: "private", Password: "secret"}, ErrorMessage: "downstream user secret", LastUsedAt: &lastUsedAt}
	out := supplierAccountView(a, owner)
	require.Nil(t, out.ProxyID)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	for _, secret := range []string{"admin-secret", "api_key", "credentials", "customer", "downstream", "supplier_user_id", "groups", "error_message", "private"} {
		require.NotContains(t, string(raw), secret)
	}
	require.Contains(t, string(raw), "supplier-note")
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.ElementsMatch(t, []string{"id", "name", "notes", "platform", "type", "status", "supplier_paused", "read_only", "proxy_id", "expires_at", "last_used_at", "created_at", "updated_at"}, mapKeys(fields))
	require.Equal(t, lastUsedAt.Format(time.RFC3339), fields["last_used_at"])

	a.Platform = PlatformGemini
	a.Type = AccountTypeServiceAccount
	a.Credentials = map[string]any{"auth_mode": "apikey", "api_key": "vertex-secret", "project_id": "private-project"}
	out = supplierAccountView(a, owner)
	raw, err = json.Marshal(out)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"auth_mode":"apikey"`)
	require.NotContains(t, string(raw), "vertex-secret")
	require.NotContains(t, string(raw), "private-project")
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

type supplierCreateTestRepo struct {
	SupplierRepository
	account *Account
}

func (r *supplierCreateTestRepo) CreateAccount(_ context.Context, _ int64, account *Account) error {
	account.ID = 1
	r.account = account
	return nil
}

func (r *supplierCreateTestRepo) GetAccount(context.Context, int64, int64) (*Account, error) {
	return r.account, nil
}

func TestSupplierCreateAccountMatchesAdminDefaultPriority(t *testing.T) {
	repo := &supplierCreateTestRepo{}
	svc := NewSupplierService(repo, nil)

	_, err := svc.CreateAccount(context.Background(), 11, SupplierAccountCreate{
		Name:        "supplier-account",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "secret"},
	})

	require.NoError(t, err)
	require.Equal(t, 1, repo.account.Priority)
}

func supplierServiceAccountJSON(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	fields := map[string]any{"type": "service_account", "project_id": "my-project", "client_email": "svc@my-project.iam.gserviceaccount.com", "private_key": pemKey, "token_uri": vertexDefaultTokenURL}
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(raw)
}

func TestSupplierServiceAccountImportDropsExternalMetadata(t *testing.T) {
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(supplierServiceAccountJSON(t)), &fields))
	fields["auth_uri"] = "https://example.invalid/oauth"
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	creds := map[string]any{"service_account_json": string(raw)}
	require.NoError(t, validateSupplierCredentials(PlatformGemini, AccountTypeServiceAccount, creds, true))
	require.NotContains(t, creds["service_account_json"], "example.invalid")
	fields["token_uri"] = "https://127.0.0.1/token"
	raw, err = json.Marshal(fields)
	require.NoError(t, err)
	creds["service_account_json"] = string(raw)
	require.Error(t, validateSupplierCredentials(PlatformGemini, AccountTypeServiceAccount, creds, true))
}

func TestSupplierGeminiVertexCredentialValidation(t *testing.T) {
	serviceAccountJSON := supplierServiceAccountJSON(t)
	require.NotContains(t, supplierCredentialKeys(PlatformAnthropic, AccountTypeServiceAccount), "api_key")
	require.NotContains(t, supplierCredentialKeys(PlatformAnthropic, AccountTypeServiceAccount), "auth_mode")
	tests := []struct {
		name     string
		platform string
		creds    map[string]any
		valid    bool
	}{
		{name: "service account", platform: PlatformGemini, creds: map[string]any{"service_account_json": serviceAccountJSON}, valid: true},
		{name: "express api key", platform: PlatformGemini, creds: map[string]any{"auth_mode": "apikey", "api_key": " key ", "tier_id": "vertex"}, valid: true},
		{name: "project api key", platform: PlatformGemini, creds: map[string]any{"auth_mode": "apikey", "api_key": "key", "project_id": "my-project", "location": "global", "tier_id": "vertex"}, valid: true},
		{name: "blank api key", platform: PlatformGemini, creds: map[string]any{"auth_mode": "apikey", "api_key": "  "}},
		{name: "missing service account", platform: PlatformGemini, creds: map[string]any{}},
		{name: "mixed credentials", platform: PlatformGemini, creds: map[string]any{"auth_mode": "apikey", "api_key": "key", "service_account_json": serviceAccountJSON}},
		{name: "invalid auth mode", platform: PlatformGemini, creds: map[string]any{"auth_mode": "other", "api_key": "key"}},
		{name: "invalid service account tier", platform: PlatformGemini, creds: map[string]any{"auth_mode": "apikey", "api_key": "key", "tier_id": "aistudio_paid"}},
		{name: "anthropic auth mode", platform: PlatformAnthropic, creds: map[string]any{"auth_mode": "apikey", "api_key": "key"}},
		{name: "anthropic api key", platform: PlatformAnthropic, creds: map[string]any{"api_key": "key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSupplierCredentials(tt.platform, AccountTypeServiceAccount, tt.creds, true)
			if tt.valid {
				require.NoError(t, err)
				if tt.creds["auth_mode"] == "apikey" {
					require.Equal(t, "key", tt.creds["api_key"])
				}
				return
			}
			require.ErrorIs(t, err, ErrSupplierInvalid)
		})
	}
}

func TestSupplierCreateGeminiVertexRejectsMixedJSONCredentials(t *testing.T) {
	serviceAccountJSON := supplierServiceAccountJSON(t)
	for _, tt := range []struct {
		name     string
		authMode string
	}{
		{name: "default auth mode"},
		{name: "explicit service account", authMode: AccountTypeServiceAccount},
	} {
		t.Run(tt.name, func(t *testing.T) {
			credentials := map[string]any{
				"service_account_json": serviceAccountJSON,
				"api_key":              "vertex-key",
			}
			if tt.authMode != "" {
				credentials["auth_mode"] = tt.authMode
			}
			repo := &supplierCreateTestRepo{}
			_, err := NewSupplierService(repo, nil).CreateAccount(context.Background(), 11, SupplierAccountCreate{
				Name: "vertex", Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: credentials,
			})

			require.ErrorIs(t, err, ErrSupplierInvalid)
			require.Nil(t, repo.account)
		})
	}
}

func TestSupplierCreateGeminiVertexRejectsInvalidAuthMode(t *testing.T) {
	repo := &supplierCreateTestRepo{}
	_, err := NewSupplierService(repo, nil).CreateAccount(context.Background(), 11, SupplierAccountCreate{
		Name:     "vertex",
		Platform: PlatformGemini,
		Type:     AccountTypeServiceAccount,
		Credentials: map[string]any{
			"auth_mode":            "foo",
			"service_account_json": supplierServiceAccountJSON(t),
		},
	})

	require.ErrorIs(t, err, ErrSupplierInvalid)
	require.Nil(t, repo.account)
}

func TestSupplierGeminiVertexEditAuthModeGuard(t *testing.T) {
	account := &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "old"}}
	require.ErrorIs(t, validateSupplierVertexEdit(account, map[string]any{"auth_mode": "service_account"}), ErrSupplierInvalid)
}

func TestSupplierBedrockAuthenticationUnaffected(t *testing.T) {
	require.NoError(t, validateSupplierCredentials(PlatformAnthropic, AccountTypeBedrock, map[string]any{
		"auth_mode": "apikey", "api_key": "bedrock-key", "aws_region": "us-east-1",
	}, true))
}

func TestSupplierGeminiVertexCreateNormalizesCredentialsAndRoutes(t *testing.T) {
	tests := []struct {
		name         string
		credentials  map[string]any
		wantURL      string
		wantLocation any
	}{
		{
			name:        "express ignores location",
			credentials: map[string]any{"auth_mode": "apikey", "api_key": " vertex-key ", "location": "us-central1", "tier_id": "vertex"},
			wantURL:     "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-3-flash-preview:generateContent",
		},
		{
			name:         "project defaults location to global",
			credentials:  map[string]any{"auth_mode": "apikey", "api_key": "vertex-key", "project_id": "my-project", "tier_id": "vertex"},
			wantURL:      "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/google/models/gemini-3-flash-preview:generateContent",
			wantLocation: "global",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &supplierCreateTestRepo{}
			svc := NewSupplierService(repo, nil)
			_, err := svc.CreateAccount(context.Background(), 11, SupplierAccountCreate{
				Name: "vertex", Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: tt.credentials,
			})
			require.NoError(t, err)
			require.Equal(t, "vertex-key", repo.account.Credentials["api_key"])
			require.Equal(t, tt.wantLocation, repo.account.Credentials["location"])
			gotURL, err := buildVertexGeminiAccountURL(repo.account, "gemini-3-flash-preview", "generateContent", false)
			require.NoError(t, err)
			require.Equal(t, tt.wantURL, gotURL)
			req := httptest.NewRequest(http.MethodPost, gotURL, nil)
			require.NoError(t, setVertexGeminiAuth(context.Background(), req, repo.account, nil))
			require.Equal(t, "vertex-key", req.Header.Get("x-goog-api-key"))
			require.Empty(t, req.Header.Get("Authorization"))
			require.NotContains(t, req.URL.RawQuery, "key=")
		})
	}
}

type supplierEditTestRepo struct {
	SupplierRepository
	a      *Account
	getErr error
}

func (r *supplierEditTestRepo) GetAccount(context.Context, int64, int64) (*Account, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.a, nil
}
func (r *supplierEditTestRepo) UpdateAccount(_ context.Context, _ int64, _ int64, in SupplierAccountUpdate) (*Account, error) {
	r.a.Credentials = mergeMap(r.a.Credentials, in.Credentials)
	return r.a, nil
}
func TestSupplierCredentialEditPreservesAdministratorEndpoint(t *testing.T) {
	owner := int64(11)
	repo := &supplierEditTestRepo{a: &Account{ID: 1, SupplierUserID: &owner, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "old", "base_url": "https://admin-configured.example/v1", "header_overrides": map[string]any{"x": "admin"}}}}
	s := NewSupplierService(repo, nil)
	_, err := s.UpdateAccount(context.Background(), owner, 1, SupplierAccountUpdate{Credentials: map[string]any{"api_key": "new"}})
	require.NoError(t, err)
	require.Equal(t, "https://admin-configured.example/v1", repo.a.Credentials["base_url"])
	require.Contains(t, repo.a.Credentials, "header_overrides")
}

func TestSupplierGeminiVertexEditRejectsAuthOrEndpointModeSwitch(t *testing.T) {
	owner := int64(11)
	parentID := int64(9)
	serviceAccountJSON := supplierServiceAccountJSON(t)
	tests := []struct {
		name        string
		account     *Account
		credentials map[string]any
		wantErr     error
	}{
		{name: "api key to service account", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "old"}}, credentials: map[string]any{"auth_mode": "service_account"}, wantErr: ErrSupplierInvalid},
		{name: "api key submits json", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "old"}}, credentials: map[string]any{"service_account_json": serviceAccountJSON}, wantErr: ErrSupplierInvalid},
		{name: "express adds project", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "old"}}, credentials: map[string]any{"project_id": "my-project"}, wantErr: ErrSupplierInvalid},
		{name: "project clears project", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "old", "project_id": "my-project", "location": "global"}}, credentials: map[string]any{"project_id": ""}, wantErr: ErrSupplierInvalid},
		{name: "json to api key", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"service_account_json": serviceAccountJSON}}, credentials: map[string]any{"auth_mode": "apikey", "api_key": "new"}, wantErr: ErrSupplierInvalid},
		{name: "json submits api key", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"service_account_json": serviceAccountJSON}}, credentials: map[string]any{"api_key": "new"}, wantErr: ErrSupplierInvalid},
		{name: "json clears project", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"service_account_json": serviceAccountJSON, "project_id": "my-project"}}, credentials: map[string]any{"project_id": ""}, wantErr: ErrSupplierInvalid},
		{name: "api key rotates", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "old"}}, credentials: map[string]any{"api_key": " new "}},
		{name: "derived remains read only", account: &Account{Platform: PlatformGemini, Type: AccountTypeServiceAccount, ParentAccountID: &parentID, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "old"}}, credentials: map[string]any{"api_key": "new"}, wantErr: ErrSupplierReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.account.ID = 1
			tt.account.SupplierUserID = &owner
			repo := &supplierEditTestRepo{a: tt.account}
			_, err := NewSupplierService(repo, nil).UpdateAccount(context.Background(), owner, 1, SupplierAccountUpdate{Credentials: tt.credentials})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "new", repo.a.Credentials["api_key"])
		})
	}
}

func TestSupplierGeminiVertexEditKeepsOwnershipBoundary(t *testing.T) {
	repo := &supplierEditTestRepo{getErr: ErrSupplierUnavailable}
	_, err := NewSupplierService(repo, nil).UpdateAccount(context.Background(), 11, 99, SupplierAccountUpdate{Credentials: map[string]any{"api_key": "new"}})
	require.ErrorIs(t, err, ErrSupplierUnavailable)
}
