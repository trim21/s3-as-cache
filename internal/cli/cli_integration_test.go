package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCLIIntegration drives both commands against a real S3 backend, so the
// flag wiring, signing and transfer paths are exercised end to end. CI runs it
// against Silo; see .github/workflows/ci.yml.
func TestCLIIntegration(t *testing.T) {
	bucketURL := os.Getenv("S3CACHE_TEST_S3")
	endpoint := os.Getenv("S3CACHE_TEST_ENDPOINT")
	if bucketURL == "" || endpoint == "" {
		t.Skip("set S3CACHE_TEST_S3 and S3CACHE_TEST_ENDPOINT to run the S3 integration tests")
	}

	key := fmt.Sprintf("cli-integration-%d", time.Now().UnixNano())
	deps := t.TempDir()
	file := filepath.Join(deps, "pkg", "index.json")
	writeTestFile(t, file, `{"version":1}`)

	location := []string{"--s3", bucketURL, "--endpoint-url", endpoint, "--force-path-style"}
	h := newRealHarness(t)

	save := append([]string{"save", "--key", key, "--path", deps}, location...)
	if got := h.mustRun(save...); got != key {
		t.Fatalf("save printed %q, want %q", got, key)
	}

	if err := os.RemoveAll(deps); err != nil {
		t.Fatal(err)
	}

	restore := append([]string{"restore", "--key", key, "--path", deps}, location...)
	if got := h.mustRun(restore...); got != key {
		t.Fatalf("restore printed %q, want %q", got, key)
	}
	if got := readTestFile(t, file); got != `{"version":1}` {
		t.Fatalf("restored content = %q", got)
	}

	lookup := append([]string{"restore", "--key", key, "--path", deps, "--lookup-only"}, location...)
	if got := h.mustRun(lookup...); got != key {
		t.Fatalf("lookup-only printed %q, want %q", got, key)
	}
}
