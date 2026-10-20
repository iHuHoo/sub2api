package middleware

import (
	"net/http"
	"path"
	"strings"
)

// ClashSubscriptionPathGuard runs before Gin's path redirects and request logging.
// Only malformed variants of this dedicated route are intercepted.
func ClashSubscriptionPathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		canonical := "/api/v1/clash/subscription"
		normalized := strings.ToLower(path.Clean(r.URL.Path))
		if r.Method != "OPTIONS" && normalized == canonical && (r.URL.Path != canonical || r.URL.RawPath != "") {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(404)
			return
		}
		next.ServeHTTP(w, r)
	})
}
