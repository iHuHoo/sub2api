package websearch

import (
	"strings"
	"testing"
)

func TestProxyParseErrorDoesNotExposeCredentials(t *testing.T) {
	_, err := newHTTPClient("http://fictional%40user:fictional%3Apass@proxy.invalid:bad")
	if err == nil {
		t.Fatal("expected invalid proxy error")
	}
	if strings.Contains(err.Error(), "fictional") {
		t.Fatalf("credential leaked: %v", err)
	}
}
