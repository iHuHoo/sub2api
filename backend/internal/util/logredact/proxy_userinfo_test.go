package logredact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactTextProxyUserinfo(t *testing.T) {
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		for _, credentials := range []string{"fictional-user:fictional-pass", "fictional%40user:fictional%3Ap%40ss%2Fword", "fictional-user"} {
			t.Run(scheme+"/"+credentials, func(t *testing.T) {
				raw := scheme + "://" + credentials + "@proxy.invalid:8080/path"
				want := scheme + "://***@proxy.invalid:8080/path"
				for _, input := range []string{raw, "proxy=" + raw, `{"proxy":"` + raw + `"}`} {
					got := RedactText(input)
					if strings.Contains(got, "fictional") || !strings.Contains(got, want) {
						t.Fatalf("userinfo not removed: %s", got)
					}
				}
				encoded, err := json.Marshal(RedactMap(map[string]any{"nested": []any{raw}}))
				if err != nil || strings.Contains(string(encoded), "fictional") {
					t.Fatalf("structured URL not redacted: %s, %v", encoded, err)
				}
			})
		}
		plain := scheme + "://proxy.invalid:8080/path?q=value"
		if got := RedactText(plain); got != plain {
			t.Fatalf("non-credential URL changed: %s", got)
		}
	}
}
