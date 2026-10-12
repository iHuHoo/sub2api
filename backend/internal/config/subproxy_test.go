//go:build unit

package config

import (
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubproxyURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		url          string
		allow, valid bool
	}{
		{"https://subproxy.example", false, true},
		{"http://subproxy:8081", false, false},
		{"http://subproxy:8081", true, true},
		{"https://user:secret@subproxy.example", false, false},
		{"https://subproxy.example?token=secret", false, false},
		{"https://subproxy.example#secret", false, false},
		{"https://subproxy.example/base", false, false},
		{"file:///secret", true, false},
		{"https://subproxy.example?", false, false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			e := (SubproxyConfig{Enabled: true, BaseURL: tc.url, AllowInternalHTTP: tc.allow}).Validate()
			if tc.valid {
				require.NoError(t, e)
			} else {
				require.Error(t, e)
				require.NotContains(t, e.Error(), "secret")
			}
		})
	}
	require.NoError(t, (SubproxyConfig{}).Validate())
}
func TestSubproxySecrets(t *testing.T) {
	t.Setenv("DIAGNOSTICS_SECRET_TEST", "test-secret")
	value, e := (SubproxySecretRef{Env: "DIAGNOSTICS_SECRET_TEST"}).Read()
	require.NoError(t, e)
	require.Equal(t, "test-secret", value)
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("file-secret\n"), 0600))
	value, e = (SubproxySecretRef{File: path}).Read()
	require.NoError(t, e)
	require.Equal(t, "file-secret", value)
	for _, r := range []SubproxySecretRef{{}, {Env: "DIAGNOSTICS_SECRET_TEST", File: path}, {File: path + "missing"}, {Env: "DIAGNOSTICS_SECRET_MISSING"}} {
		_, e = r.Read()
		require.Error(t, e)
		require.NotContains(t, e.Error(), path)
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 4097)), 0600))
	_, e = (SubproxySecretRef{File: path}).Read()
	require.Error(t, e)
}
func TestSubproxyEnvironmentConfiguration(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()
	t.Setenv("SUBPROXY_ENABLED", "true")
	t.Setenv("SUBPROXY_TOKEN_ENV", "DIAGNOSTICS_SECRET_TEST")
	t.Setenv("SUBPROXY_HMAC_SECRET_FILE", "/run/secrets/hmac")
	var c Config
	require.NoError(t, viper.Unmarshal(&c))
	require.True(t, c.Subproxy.Enabled)
	require.Equal(t, "DIAGNOSTICS_SECRET_TEST", c.Subproxy.Token.Env)
	require.Equal(t, "/run/secrets/hmac", c.Subproxy.HMACSecret.File)
}
