package config

import (
	"fmt"
	"net/url"
	"strings"
)

type ClashSubscriptionConfig struct {
	Enabled       bool   `mapstructure:"enabled"`
	PublicBaseURL string `mapstructure:"public_base_url"`
}

func (c ClashSubscriptionConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(c.PublicBaseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(c.PublicBaseURL, "#") {
		return fmt.Errorf("clash_subscription.public_base_url must be an HTTPS URL without credentials, query or fragment")
	}
	// v1 exposes a single fixed route. Prefix rewrites need a separately reviewed deployment.
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("clash_subscription.public_base_url path prefixes are not supported")
	}
	return nil
}
