package config

import (
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
)

type SubproxySecretRef struct {
	Env  string `mapstructure:"env"`
	File string `mapstructure:"file"`
}
type SubproxyConfig struct {
	Enabled           bool              `mapstructure:"enabled"`
	BaseURL           string            `mapstructure:"base_url"`
	AllowInternalHTTP bool              `mapstructure:"allow_internal_http"`
	Token             SubproxySecretRef `mapstructure:"token"`
	HMACSecret        SubproxySecretRef `mapstructure:"hmac_secret"`
}

func (c SubproxyConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("subproxy: invalid base_url")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !c.AllowInternalHTTP) {
		return errors.New("subproxy: HTTPS required unless same-host internal HTTP is explicitly enabled")
	}
	return nil
}
func (r SubproxySecretRef) Read() (string, error) {
	fail := errors.New("subproxy: secret unavailable or invalid")
	if (r.Env == "") == (r.File == "") {
		return "", fail
	}
	value := ""
	if r.Env != "" {
		value = os.Getenv(r.Env)
	} else {
		f, e := os.Open(r.File)
		if e != nil {
			return "", fail
		}
		defer func() { _ = f.Close() }()
		b, e := io.ReadAll(io.LimitReader(f, 4097))
		if e != nil || len(b) > 4096 {
			return "", fail
		}
		value = string(b)
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, " \t\r\n") {
		return "", fail
	}
	return value, nil
}
