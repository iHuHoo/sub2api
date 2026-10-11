package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCleanPageImageRelativePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "single filename", in: "logo.png", want: "logo.png", ok: true},
		{name: "nested path", in: "images/logo.png", want: filepath.Join("images", "logo.png"), ok: true},
		{name: "dot prefix", in: "./logo.png", want: "logo.png", ok: true},
		{name: "url escaped slash", in: "images%2Flogo.png", want: filepath.Join("images", "logo.png"), ok: true},
		{name: "parent traversal", in: "../secret.png", ok: false},
		{name: "encoded parent traversal", in: "%2e%2e/secret.png", ok: false},
		{name: "backslash traversal", in: `images\secret.png`, ok: false},
		{name: "absolute path", in: "/etc/passwd", ok: false},
		{name: "encoded absolute path", in: "%2fetc/passwd", ok: false},
		{name: "encoded nul byte", in: "logo.png%00", ok: false},
		{name: "invalid escape", in: "logo.png%zz", ok: false},
		{name: "empty path", in: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := cleanPageImageRelativePath(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolvePageImagePath(t *testing.T) {
	root := t.TempDir()
	pagesDir := filepath.Join(root, "pages")
	base := filepath.Join(pagesDir, "guide")
	if err := os.MkdirAll(filepath.Join(base, "images"), 0755); err != nil {
		t.Fatalf("create images dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "logo.png"), []byte("fake"), 0644); err != nil {
		t.Fatalf("create direct image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "images", "logo.png"), []byte("fake"), 0644); err != nil {
		t.Fatalf("create image: %v", err)
	}

	got, ok := resolvePageImagePath(pagesDir, base, "logo.png")
	if !ok {
		t.Fatal("expected direct image path to be accepted")
	}
	want := mustEvalSymlinks(t, filepath.Join(base, "logo.png"))
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	got, ok = resolvePageImagePath(pagesDir, base, "images/logo.png")
	if !ok {
		t.Fatal("expected nested image path to be accepted")
	}
	want = mustEvalSymlinks(t, filepath.Join(base, "images", "logo.png"))
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	if got, ok := resolvePageImagePath(pagesDir, base, "../guide.md"); ok {
		t.Fatalf("expected traversal to be rejected, got %q", got)
	}
}

func TestResolvePageImagePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	pagesDir := filepath.Join(root, "pages")
	base := filepath.Join(pagesDir, "guide")
	outside := filepath.Join(root, "outside")

	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatalf("create page dir: %v", err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.png"), []byte("secret"), 0644); err != nil {
		t.Fatalf("create outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "images")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if got, ok := resolvePageImagePath(pagesDir, base, "images/secret.png"); ok {
		t.Fatalf("expected symlink escape to be rejected, got %q", got)
	}
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()

	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks for %q: %v", path, err)
	}
	return realPath
}

func TestRegisterPageRoutesSupplierBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dataDir := t.TempDir()
	pagesDir := filepath.Join(dataDir, "pages")
	for _, slug := range []string{"customer-guide", "admin-guide"} {
		require.NoError(t, os.MkdirAll(filepath.Join(pagesDir, slug), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(pagesDir, slug+".md"), []byte("# Private markdown"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(pagesDir, slug, "logo.png"), []byte("page image"), 0644))
	}

	for _, backendMode := range []string{"false", "true"} {
		t.Run("backend_mode="+backendMode, func(t *testing.T) {
			settings := service.NewSettingService(&oauthPendingFlowSettingRepoStub{values: map[string]string{
				service.SettingKeyBackendModeEnabled: backendMode,
				service.SettingKeyCustomMenuItems:    `[{"url":"md:customer-guide","visibility":"user"},{"url":"md:admin-guide","visibility":"admin"}]`,
			}}, nil)
			r := gin.New()
			jwtAuth := func(c *gin.Context) {
				role := c.GetHeader("X-Test-Role")
				if role == "" {
					c.AbortWithStatus(http.StatusUnauthorized)
					return
				}
				c.Set(string(middleware.ContextKeyUserRole), role)
				c.Next()
			}
			adminAuth := func(c *gin.Context) { c.AbortWithStatus(http.StatusForbidden) }
			RegisterPageRoutes(r.Group("/api/v1"), dataDir, jwtAuth, adminAuth, settings)

			for _, tt := range []struct {
				name string
				role string
				path string
				want int
				body string
			}{
				{"supplier consumer page", service.RoleSupplier, "/customer-guide", http.StatusForbidden, ""},
				{"user consumer page", service.RoleUser, "/customer-guide", http.StatusOK, "# Private markdown"},
				{"admin consumer page", service.RoleAdmin, "/customer-guide", http.StatusOK, "# Private markdown"},
				{"anonymous consumer page", "", "/customer-guide", http.StatusUnauthorized, ""},
				{"supplier admin page", service.RoleSupplier, "/admin-guide", http.StatusForbidden, ""},
				{"user admin page", service.RoleUser, "/admin-guide", http.StatusNotFound, ""},
				{"admin admin page", service.RoleAdmin, "/admin-guide", http.StatusOK, "# Private markdown"},
				{"anonymous consumer image", "", "/customer-guide/images/logo.png", http.StatusOK, "page image"},
				{"anonymous admin image", "", "/admin-guide/images/logo.png", http.StatusNotFound, ""},
			} {
				t.Run(tt.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, "/api/v1/pages"+tt.path, nil)
					req.Header.Set("X-Test-Role", tt.role)
					w := httptest.NewRecorder()
					r.ServeHTTP(w, req)
					require.Equal(t, tt.want, w.Code)
					if tt.body != "" {
						require.Equal(t, tt.body, w.Body.String())
					} else {
						require.NotContains(t, w.Body.String(), "# Private markdown")
					}
				})
			}
		})
	}
}
