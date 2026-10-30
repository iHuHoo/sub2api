package proxyurl

import (
	"strings"
	"testing"
)

func TestParseErrorsDoNotExposeCredentials(t *testing.T) {
	for _, raw := range []string{"http://fictional%40user:fictional%3Apass@proxy.invalid:bad", "http://fictional-user:fictional-pass@", "http://fictional-user:fictional-pass@proxy.invalid/%zz"} {
		_, _, err := Parse(raw)
		if err == nil {
			t.Fatal("expected invalid proxy error")
		}
		if strings.Contains(err.Error(), "fictional") {
			t.Fatalf("credential leaked: %v", err)
		}
	}
}
