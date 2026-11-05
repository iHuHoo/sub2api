// Package claudetest installs the repository's Claude client profile for tests. It is
// imported only from _test.go files; production code loads the profile at startup.
package claudetest

import (
	"path/filepath"
	"runtime"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// ProfilePath is the shipped profile file inside the repository.
func ProfilePath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "resources", "claude-client-profile", "profile.json")
}

// Install loads and activates the shipped profile; it panics when the file is invalid.
func Install() {
	p, err := claude.LoadClientProfileFile(ProfilePath())
	if err != nil {
		panic(err)
	}
	if err := claude.SetActiveClientProfile(p); err != nil {
		panic(err)
	}
}

// Importing this package for side effects (`import _ ".../claudetest"`) from a _test.go
// file installs the shipped profile before any test in that package runs.
func init() { Install() }
