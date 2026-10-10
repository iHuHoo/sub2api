//go:build unit

package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/stretchr/testify/require"
	"testing"
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
	a := &Account{ID: 1, Name: "a", SupplierUserID: &owner, Notes: &adminNote, SupplierNotes: &note, Credentials: map[string]any{"api_key": "secret"}, Extra: map[string]any{"customer": "private"}, ProxyID: &proxyID, Proxy: &Proxy{ID: proxyID, SupplierUserID: &foreign, Host: "private", Password: "secret"}, ErrorMessage: "downstream user secret"}
	out := supplierAccountView(a, owner)
	require.Nil(t, out.ProxyID)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	for _, secret := range []string{"admin-secret", "api_key", "credentials", "customer", "downstream", "supplier_user_id", "groups", "error_message", "private"} {
		require.NotContains(t, string(raw), secret)
	}
	require.Contains(t, string(raw), "supplier-note")
}

func TestSupplierServiceAccountImportDropsExternalMetadata(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	fields := map[string]any{"type": "service_account", "project_id": "my-project", "client_email": "svc@my-project.iam.gserviceaccount.com", "private_key": pemKey, "token_uri": vertexDefaultTokenURL, "auth_uri": "https://example.invalid/oauth"}
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

type supplierEditTestRepo struct {
	SupplierRepository
	a *Account
}

func (r *supplierEditTestRepo) GetAccount(context.Context, int64, int64) (*Account, error) {
	return r.a, nil
}
func (r *supplierEditTestRepo) UpdateAccount(_ context.Context, _ int64, _ int64, in SupplierAccountUpdate) (*Account, error) {
	r.a.Credentials = mergeMap(r.a.Credentials, in.Credentials)
	return r.a, nil
}
func TestSupplierCredentialEditPreservesAdministratorEndpoint(t *testing.T) {
	owner := int64(11)
	repo := &supplierEditTestRepo{a: &Account{ID: 1, SupplierUserID: &owner, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "old", "base_url": "https://admin-configured.example/v1", "header_overrides": map[string]any{"x": "admin"}}}}
	s := NewSupplierService(repo)
	_, err := s.UpdateAccount(context.Background(), owner, 1, SupplierAccountUpdate{Credentials: map[string]any{"api_key": "new"}})
	require.NoError(t, err)
	require.Equal(t, "https://admin-configured.example/v1", repo.a.Credentials["base_url"])
	require.Contains(t, repo.a.Credentials, "header_overrides")
}
