package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCLIIntegration drives both commands against a real S3 backend, so the
// option wiring, signing and transfer paths are exercised end to end. CI runs it
// against Silo; see .github/workflows/ci.yml.
func TestCLIIntegration(t *testing.T) {
	bucketURL := os.Getenv("S3CACHE_TEST_S3")
	endpoint := os.Getenv("S3CACHE_TEST_ENDPOINT")
	accessKey := os.Getenv("S3CACHE_TEST_ACCESS_KEY")
	secretKey := os.Getenv("S3CACHE_TEST_SECRET_KEY")
	if bucketURL == "" || endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("set S3CACHE_TEST_S3, S3CACHE_TEST_ENDPOINT, S3CACHE_TEST_ACCESS_KEY and S3CACHE_TEST_SECRET_KEY to run the S3 integration tests")
	}

	key := fmt.Sprintf("cli-integration-%d", time.Now().UnixNano())
	deps := t.TempDir()
	file := filepath.Join(deps, "pkg", "index.json")
	writeTestFile(t, file, `{"version":1}`)

	h := newRealHarness(t)
	h.env(bucketURL, key)
	t.Setenv(envAccessKey, accessKey)
	t.Setenv(envSecretKey, secretKey)
	// No envForcePathStyle: an endpoint must be enough to select path style.
	t.Setenv(envEndpoint, endpoint)

	if got := h.mustRun("save", "--path", deps); got != key {
		t.Fatalf("save printed %q, want %q", got, key)
	}

	if err := os.RemoveAll(deps); err != nil {
		t.Fatal(err)
	}
	if got := h.mustRun("restore"); got != key {
		t.Fatalf("restore printed %q, want %q", got, key)
	}
	if got := readTestFile(t, file); got != `{"version":1}` {
		t.Fatalf("restored content = %q", got)
	}
}
