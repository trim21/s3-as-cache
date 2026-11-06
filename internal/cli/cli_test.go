package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/trim21/s3-as-cache/internal/store"
)

var (
	envS3             = envVarName(optS3)
	envKey            = envVarName(optKey)
	envRegion         = envVarName(optRegion)
	envEndpoint       = envVarName(optEndpoint)
	envForcePathStyle = envVarName(optForcePathStyle)
	envCompression    = envVarName(optCompression)
	envCrossPlatform  = envVarName(optCrossPlatform)
	envQuiet          = envVarName(optQuiet)
	envVerbose        = envVarName(optVerbose)
	envOverwrite      = envVarName(optOverwrite)
	envAccessKey      = envVarName(optAccessKey)
	envSecretKey      = envVarName(optSecretKey)
)

var allEnvVars = []string{
	envS3, envKey, envAccessKey, envSecretKey, envVarName(optSessionToken), envRegion,
	envEndpoint, envForcePathStyle, envCompression, envCrossPlatform, envQuiet,
	envVerbose, envOverwrite,
}

// clearEnv keeps the developer's own S3CACHE_* settings out of the tests.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range allEnvVars {
		t.Setenv(name, "")
	}
}

type harness struct {
	t *testing.T
	// store is nil when the command must reach for the real S3 backend.
	store  *store.Local
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clearEnv(t)
	return &harness{t: t, store: &store.Local{Root: t.TempDir()}}
}

func newRealHarness(t *testing.T) *harness {
	t.Helper()
	clearEnv(t)
	return &harness{t: t}
}

// env points the command at a bucket and a key, with placeholder credentials.
func (h *harness) env(bucketURL, key string) {
	h.t.Helper()
	h.t.Setenv(envS3, bucketURL)
	h.t.Setenv(envKey, key)
	h.t.Setenv(envAccessKey, "test-access-key")
	h.t.Setenv(envSecretKey, "test-secret-key")
}

func (h *harness) run(args ...string) (int, string) {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()

	opts := Options{Stdout: &h.stdout, Stderr: &h.stderr}
	if h.store != nil {
		local := h.store
		opts.OpenStore = func(store.S3Config) (store.Store, error) {
			return local, nil
		}
	}

	code := Run(context.Background(), args, opts)
	return code, strings.TrimSpace(h.stdout.String())
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	code, stdout := h.run(args...)
	if code != 0 {
		h.t.Fatalf("s3cache %s exited with %d:\n%s", strings.Join(args, " "), code, h.stderr.String())
	}
	return stdout
}

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

func TestSaveThenRestore(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "deps-1")

	deps := t.TempDir()
	file := filepath.Join(deps, "pkg", "index.json")
	writeTestFile(t, file, `{"version":1}`)

	if got := h.mustRun("save", "--path", deps); got != "deps-1" {
		t.Fatalf("save printed %q, want the cache key", got)
	}

	if err := os.RemoveAll(deps); err != nil {
		t.Fatal(err)
	}
	if got := h.mustRun("restore"); got != "deps-1" {
		t.Fatalf("restore printed %q, want the matched key", got)
	}
	if got := readTestFile(t, file); got != `{"version":1}` {
		t.Fatalf("restored content = %q", got)
	}
}

func TestRestoreFallsBackToPrefixKey(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "linux-old")

	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")
	h.mustRun("save", "--path", deps)

	h.t.Setenv(envKey, "linux-new")

	code, stdout := h.run("restore", "--restore-key", "linux-")
	if code != 0 || stdout != "linux-old" {
		t.Fatalf("restore = (%d, %q), want (0, %q)", code, stdout, "linux-old")
	}
	if !strings.Contains(h.stderr.String(), "falling back to linux-old") {
		t.Fatalf("expected a fallback notice, got:\n%s", h.stderr.String())
	}
}

func TestRestoreMiss(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "absent")

	code, stdout := h.run("restore")
	if code != 0 || stdout != "" {
		t.Fatalf("miss = (%d, %q), want a silent success", code, stdout)
	}
}

func TestSaveKeepsTheFirstEntryUnlessOverwriting(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "k1")

	deps := t.TempDir()
	file := filepath.Join(deps, "lock.json")
	writeTestFile(t, file, "first")
	h.mustRun("save", "--path", deps)

	writeTestFile(t, file, "second")
	h.mustRun("save", "--path", deps)
	if !strings.Contains(h.stderr.String(), "already exists") {
		t.Fatalf("expected the second save to be skipped, got:\n%s", h.stderr.String())
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	h.mustRun("restore")
	if got := readTestFile(t, file); got != "first" {
		t.Fatalf("restored %q, want the first save", got)
	}

	writeTestFile(t, file, "third")
	h.t.Setenv(envOverwrite, "true")
	h.mustRun("save", "--path", deps)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	h.mustRun("restore")
	if got := readTestFile(t, file); got != "third" {
		t.Fatalf("restored %q, want the overwritten save", got)
	}
}

// The probe must run before the archive work, otherwise a repeated save pays for
// a whole tar and compression it will never upload.
func TestSaveFindsTheExistingEntryBeforeArchiving(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "k1")

	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")
	h.mustRun("save", "--path", deps)

	// Nothing matches this path: reaching the archive stage would report a skip.
	if got := h.mustRun("save", "--path", filepath.Join(deps, "absent")); got != "" {
		t.Fatalf("save printed %q, want nothing", got)
	}
	if !strings.Contains(h.stderr.String(), "already exists") {
		t.Fatalf("expected the existing entry to be found before archiving, got:\n%s", h.stderr.String())
	}
}

func TestCachesAreStoredPerPlatform(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "k1")

	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")
	h.mustRun("save", "--path", deps)

	objects, err := h.store.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if len(objects) != 1 || objects[0].Key != "ci/"+platform+"/k1.tar.zst" {
		t.Fatalf("objects = %+v, want ci/%s/k1.tar.zst", objects, platform)
	}

	// A cross-platform binary must not pick up a platform specific archive.
	h.t.Setenv(envCrossPlatform, "true")
	if code, stdout := h.run("restore"); code != 0 || stdout != "" {
		t.Fatalf("S3CACHE_CROSS_PLATFORM must not match a platform specific cache: (%d, %q)", code, stdout)
	}
}

func TestCrossPlatformCachesLiveAboveThePlatformDirectory(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "k1")
	h.t.Setenv(envCrossPlatform, "true")

	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")
	h.mustRun("save", "--path", deps)

	objects, err := h.store.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Key != "ci/k1.tar.zst" {
		t.Fatalf("objects = %+v, want ci/k1.tar.zst", objects)
	}
}

func TestRestoreNeedsNoPathFlag(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "k1")

	first := t.TempDir()
	second := t.TempDir()
	writeTestFile(t, filepath.Join(first, "a.txt"), "a")
	writeTestFile(t, filepath.Join(second, "b.txt"), "b")
	h.mustRun("save", "--path", first, "--path", second)

	for _, dir := range []string{first, second} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}

	if got := h.mustRun("restore"); got != "k1" {
		t.Fatalf("restore printed %q, want the matched key", got)
	}
	if readTestFile(t, filepath.Join(first, "a.txt")) != "a" {
		t.Fatal("first saved path was not restored")
	}
	if readTestFile(t, filepath.Join(second, "b.txt")) != "b" {
		t.Fatal("second saved path was not restored")
	}
}

func TestSaveSkipsWhenNothingMatches(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "k1")

	if got := h.mustRun("save", "--path", filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Fatalf("save printed %q, want nothing", got)
	}
	if !strings.Contains(h.stderr.String(), "skipping cache save") {
		t.Fatalf("expected a skip notice, got:\n%s", h.stderr.String())
	}
}

func TestOptionsReachTheStore(t *testing.T) {
	clearEnv(t)
	t.Setenv(envS3, "s3://bucket/ci/prefix")
	t.Setenv(envKey, "k1")
	t.Setenv(envAccessKey, "ak")
	t.Setenv(envSecretKey, "sk")
	t.Setenv(envVarName(optSessionToken), "token")
	t.Setenv(envRegion, "eu-west-3")
	t.Setenv(envEndpoint, "https://s3.example.com")
	t.Setenv(envForcePathStyle, "true")
	t.Setenv(envQuiet, "true")

	local := &store.Local{Root: t.TempDir()}
	var captured store.S3Config
	var stdout, stderr bytes.Buffer

	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")

	code := Run(context.Background(), []string{"save", "--path", deps}, Options{
		Stdout: &stdout,
		Stderr: &stderr,
		OpenStore: func(cfg store.S3Config) (store.Store, error) {
			captured = cfg
			return local, nil
		},
	})
	if code != 0 {
		t.Fatalf("save exited with %d:\n%s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("S3CACHE_QUIET was ignored, stderr:\n%s", stderr.String())
	}
	if captured.Bucket != "bucket" || captured.Prefix != "ci/prefix" {
		t.Fatalf("bucket/prefix = %q/%q", captured.Bucket, captured.Prefix)
	}
	if captured.Region != "eu-west-3" || captured.Endpoint != "https://s3.example.com" || !captured.ForcePathStyle {
		t.Fatalf("unexpected S3 config %+v", captured)
	}
	if captured.AccessKey != "ak" || captured.SecretKey != "sk" || captured.SessionToken != "token" {
		t.Fatalf("credentials did not reach the store: %+v", captured)
	}
}

func TestCompressionFromEnvironment(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "k1")
	h.t.Setenv(envCompression, "gzip")

	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")
	h.mustRun("save", "--path", deps)

	objects, err := h.store.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || !strings.HasSuffix(objects[0].Key, ".tar.gz") {
		t.Fatalf("objects = %+v, want one .tar.gz archive", objects)
	}

	if got := h.mustRun("restore"); got != "k1" {
		t.Fatalf("restore printed %q", got)
	}
}

func TestKeyTemplates(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "")
	h.t.Setenv("CACHE_NAMESPACE", "ci")

	workspace := t.TempDir()
	lock := filepath.Join(workspace, "Cargo.lock")
	writeTestFile(t, lock, "lock-v1")
	deps := filepath.Join(workspace, "target")
	writeTestFile(t, filepath.Join(deps, "artifact"), "built")

	t.Chdir(workspace)
	template := `linux-{{ env "CACHE_NAMESPACE" }}-{{ hashFiles "Cargo.lock" }}`
	h.t.Setenv(envKey, template)

	got := h.mustRun("save", "--path", deps)
	if !strings.HasPrefix(got, "linux-ci-") || strings.Contains(got, "{{") {
		t.Fatalf("save printed %q, want the expanded key", got)
	}

	if err := os.RemoveAll(deps); err != nil {
		t.Fatal(err)
	}
	if matched := h.mustRun("restore"); matched != got {
		t.Fatalf("restore matched %q, want %q", matched, got)
	}
	if content := readTestFile(t, filepath.Join(deps, "artifact")); content != "built" {
		t.Fatalf("restored content = %q", content)
	}

	writeTestFile(t, lock, "lock-v2")
	if code, stdout := h.run("restore"); code != 0 || stdout != "" {
		t.Fatalf("a changed lockfile must miss: (%d, %q)", code, stdout)
	}
}

func TestKeyTemplateErrors(t *testing.T) {
	tests := map[string]string{
		"unset env":             `linux-{{ env "S3CACHE_UNSET_KEY_VAR" }}`,
		"unknown function":      `linux-{{ runner "os" }}`,
		"unmatched glob":        `{{ hashFiles "definitely-missing.lock" }}`,
		"empty after expansion": `{{ env "S3CACHE_EMPTY_KEY_VAR" }}`,
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.env("s3://bucket/ci", key)
			h.t.Setenv("S3CACHE_EMPTY_KEY_VAR", "  ")

			code, stdout := h.run("save", "--path", t.TempDir())
			if code == 0 {
				t.Fatalf("expected a non-zero exit for %q", key)
			}
			if stdout != "" {
				t.Fatalf("unexpected stdout %q", stdout)
			}
		})
	}
}

func TestRestoreKeyTemplates(t *testing.T) {
	h := newHarness(t)
	h.env("s3://bucket/ci", "main-linux-old")
	h.t.Setenv("CACHE_BRANCH", "main")

	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")
	h.mustRun("save", "--path", deps)

	h.t.Setenv(envKey, "main-linux-new")

	code, stdout := h.run("restore", "--restore-key", `{{ env "CACHE_BRANCH" }}-linux-`)
	if code != 0 || stdout != "main-linux-old" {
		t.Fatalf("restore = (%d, %q), want (0, %q)", code, stdout, "main-linux-old")
	}
}

func TestUsageErrors(t *testing.T) {
	tests := map[string][]string{
		"no command":      {},
		"unknown command": {"cheese"},
		"missing path":    {"save"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.env("s3://bucket/ci", "k1")
			if code, stdout := h.run(args...); code != 2 || stdout != "" {
				t.Fatalf("exited with %d and printed %q, want code 2 and no stdout", code, stdout)
			}
		})
	}
}

func TestMissingOptions(t *testing.T) {
	tests := map[string]struct {
		s3, key, accessKey, secretKey, wantVar string
	}{
		"no bucket":     {s3: "", key: "k1", accessKey: "ak", secretKey: "sk", wantVar: envS3},
		"no key":        {s3: "s3://bucket/ci", key: "   ", accessKey: "ak", secretKey: "sk", wantVar: envKey},
		"no access key": {s3: "s3://bucket/ci", key: "k1", accessKey: "", secretKey: "sk", wantVar: envAccessKey},
		"no secret key": {s3: "s3://bucket/ci", key: "k1", accessKey: "ak", secretKey: "", wantVar: envSecretKey},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.t.Setenv(envS3, tt.s3)
			h.t.Setenv(envKey, tt.key)
			h.t.Setenv(envAccessKey, tt.accessKey)
			h.t.Setenv(envSecretKey, tt.secretKey)

			code, stdout := h.run("save", "--path", t.TempDir())
			if code != 2 || stdout != "" {
				t.Fatalf("exited with %d and printed %q, want code 2", code, stdout)
			}
			if !strings.Contains(h.stderr.String(), tt.wantVar) {
				t.Fatalf("the error should name %s, got:\n%s", tt.wantVar, h.stderr.String())
			}
		})
	}
}

func TestInvalidEnvironmentValues(t *testing.T) {
	tests := map[string]struct {
		name  string
		value string
	}{
		"unparsable boolean":  {name: envQuiet, value: "not-a-bool"},
		"unknown compression": {name: envCompression, value: "lz4"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.env("s3://bucket/ci", "k1")
			h.t.Setenv(tt.name, tt.value)

			code, stdout := h.run("save", "--path", t.TempDir())
			if code != 2 || stdout != "" {
				t.Fatalf("exited with %d and printed %q, want code 2", code, stdout)
			}
			if !strings.Contains(h.stderr.String(), tt.name) && !strings.Contains(h.stderr.String(), tt.value) {
				t.Fatalf("the error should name the variable or value, got:\n%s", h.stderr.String())
			}
		})
	}
}

func TestHelp(t *testing.T) {
	h := newHarness(t)

	if code, _ := h.run("--help"); code != 0 {
		t.Fatalf("--help exited with %d", code)
	}
	if !strings.Contains(h.stdout.String(), "S3CACHE_S3") {
		t.Fatalf("help output should list the environment options:\n%s", h.stdout.String())
	}

	if code, _ := h.run("save", "--help"); code != 0 {
		t.Fatalf("save --help exited with %d", code)
	}
	if !strings.Contains(h.stdout.String(), "--path") {
		t.Fatalf("save help should document --path:\n%s", h.stdout.String())
	}
	if code, _ := h.run("restore", "--help"); code != 0 {
		t.Fatalf("restore --help exited with %d", code)
	}
	if !strings.Contains(h.stdout.String(), "--restore-key") || !strings.Contains(h.stdout.String(), "exits 0") {
		t.Fatalf("restore help is incomplete:\n%s", h.stdout.String())
	}
}

func TestPathStyleFollowsTheEndpoint(t *testing.T) {
	tests := map[string]struct {
		endpoint       string
		forcePathStyle string
		want           bool
	}{
		"aws default":              {endpoint: "", forcePathStyle: "", want: false},
		"endpoint turns it on":     {endpoint: "http://127.0.0.1:9000", forcePathStyle: "", want: true},
		"endpoint, explicitly off": {endpoint: "http://127.0.0.1:9000", forcePathStyle: "false", want: false},
		"aws, explicitly on":       {endpoint: "", forcePathStyle: "true", want: true},
		"endpoint, explicitly on":  {endpoint: "http://127.0.0.1:9000", forcePathStyle: "true", want: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(envS3, "s3://bucket/ci")
			t.Setenv(envKey, "k1")
			t.Setenv(envAccessKey, "ak")
			t.Setenv(envSecretKey, "sk")
			t.Setenv(envEndpoint, tt.endpoint)
			t.Setenv(envForcePathStyle, tt.forcePathStyle)

			var captured store.S3Config
			code := Run(context.Background(), []string{"restore"}, Options{
				Stdout: &bytes.Buffer{},
				Stderr: &bytes.Buffer{},
				OpenStore: func(cfg store.S3Config) (store.Store, error) {
					captured = cfg
					return &store.Local{Root: t.TempDir()}, nil
				},
			})
			if code != 0 {
				t.Fatalf("restore exited with %d", code)
			}
			if captured.ForcePathStyle != tt.want {
				t.Fatalf("force path style = %t, want %t", captured.ForcePathStyle, tt.want)
			}
		})
	}
}
