package service

import (
	"os"
	"os/exec"
	"regexp"
	"testing"
)

// Isolate process-wide state changes and memory measurements from other tests' workers.
func runTestInIsolatedProcess(t *testing.T) bool {
	t.Helper()
	const key = "SUB2API_ISOLATED_MEMORY_TEST"
	if os.Getenv(key) == t.Name() {
		return false
	}
	// #nosec G702 -- Re-executes this test binary with an exact test-name filter.
	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), key+"="+t.Name())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated test failed: %v\n%s", err, output)
	}
	return true
}
