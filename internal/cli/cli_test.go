package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trim21/s3-as-cache/internal/store"
)

type harness struct {
	t *testing.T
	// store is nil when the command must reach for the real S3 backend.
	store  *store.Local
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, store: &store.Local{Root: t.TempDir()}}
}

func newRealHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t}
}

func (h *harness) run(args ...string) (int, string) {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	opts := Options{Stdout: &h.stdout, Stderr: &h.stderr}
	if h.store != nil {
		local := h.store
		opts.OpenStore = func(context.Context, store.S3Config) (store.Store, error) {
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
	deps := t.TempDir()
	file := filepath.Join(deps, "pkg", "index.json")
	writeTestFile(t, file, `{"version":1}`)

	args := []string{"save", "--s3", "s3://bucket/ci", "--key", "deps-1", "--path", deps}
	if got := h.mustRun(args...); got != "deps-1" {
		t.Fatalf("save printed %q, want the cache key", got)
	}

	if err := os.RemoveAll(deps); err != nil {
		t.Fatal(err)
	}

	restore := []string{"restore", "--s3", "s3://bucket/ci", "--key", "deps-1", "--path", deps}
	if got := h.mustRun(restore...); got != "deps-1" {
		t.Fatalf("restore printed %q, want the matched key", got)
	}
	if got := readTestFile(t, file); got != `{"version":1}` {
		t.Fatalf("restored content = %q", got)
	}
}

func TestRestoreFallsBackToPrefixKey(t *testing.T) {
	h := newHarness(t)
	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")

	h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "linux-old", "--path", deps)

	code, stdout := h.run("restore", "--s3", "s3://bucket/ci", "--key", "linux-new",
		"--restore-key", "linux-", "--path", deps, "--lookup-only")
	if code != 0 || stdout != "linux-old" {
		t.Fatalf("lookup-only = (%d, %q), want (0, %q)", code, stdout, "linux-old")
	}
	if !strings.Contains(h.stderr.String(), "falling back to linux-old") {
		t.Fatalf("expected a fallback notice, got:\n%s", h.stderr.String())
	}
}

func TestRestoreMiss(t *testing.T) {
	h := newHarness(t)
	deps := t.TempDir()

	code, stdout := h.run("restore", "--s3", "s3://bucket/ci", "--key", "absent", "--path", deps)
	if code != 0 || stdout != "" {
		t.Fatalf("plain miss = (%d, %q), want a silent success", code, stdout)
	}

	code, _ = h.run("restore", "--s3", "s3://bucket/ci", "--key", "absent", "--path", deps, "--fail-on-miss")
	if code != 1 {
		t.Fatalf("--fail-on-miss exited with %d, want 1", code)
	}
}

func TestSaveKeepsTheFirstEntryUnlessOverwriting(t *testing.T) {
	h := newHarness(t)
	deps := t.TempDir()
	file := filepath.Join(deps, "lock.json")
	writeTestFile(t, file, "first")

	h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps)

	writeTestFile(t, file, "second")
	h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps)
	if !strings.Contains(h.stderr.String(), "already exists") {
		t.Fatalf("expected the second save to be skipped, got:\n%s", h.stderr.String())
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	h.mustRun("restore", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps)
	if got := readTestFile(t, file); got != "first" {
		t.Fatalf("restored %q, want the first save", got)
	}

	writeTestFile(t, file, "third")
	h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps, "--overwrite")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	h.mustRun("restore", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps)
	if got := readTestFile(t, file); got != "third" {
		t.Fatalf("restored %q, want the overwritten save", got)
	}
}

func TestVersionSeparatesDifferentPathSets(t *testing.T) {
	h := newHarness(t)
	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")

	h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps)

	code, stdout := h.run("restore", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps+"/a.txt")
	if code != 0 || stdout != "" {
		t.Fatalf("a different --path must not match: (%d, %q)", code, stdout)
	}
}

func TestSaveSkipsWhenNothingMatches(t *testing.T) {
	h := newHarness(t)
	missing := filepath.Join(t.TempDir(), "absent")

	if got := h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "k1", "--path", missing); got != "" {
		t.Fatalf("save printed %q, want nothing", got)
	}
	if !strings.Contains(h.stderr.String(), "skipping cache save") {
		t.Fatalf("expected a skip notice, got:\n%s", h.stderr.String())
	}
}

func TestLookupOnlyDoesNotWriteFiles(t *testing.T) {
	h := newHarness(t)
	deps := t.TempDir()
	file := filepath.Join(deps, "a.txt")
	writeTestFile(t, file, "a")

	h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps)
	if err := os.RemoveAll(deps); err != nil {
		t.Fatal(err)
	}

	if got := h.mustRun("restore", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps, "--lookup-only"); got != "k1" {
		t.Fatalf("lookup-only printed %q", got)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("--lookup-only must not extract files")
	}
}

func TestInvalidInvocations(t *testing.T) {
	tests := map[string][]string{
		"no command":       {},
		"unknown command":  {"cheese"},
		"missing key":      {"save", "--s3", "s3://bucket/ci", "--path", "/tmp"},
		"missing path":     {"save", "--s3", "s3://bucket/ci", "--key", "k1"},
		"missing location": {"save", "--key", "k1", "--path", "/tmp"},
		"bad compression":  {"save", "--s3", "s3://bucket/ci", "--key", "k1", "--path", "/tmp", "--compression", "lz4"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			code, stdout := h.run(args...)
			if code != 2 || stdout != "" {
				t.Fatalf("exited with %d and printed %q, want code 2 and no stdout", code, stdout)
			}
		})
	}
}

func TestHelp(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.run("--help"); code != 0 {
		t.Fatalf("--help exited with %d", code)
	}
	if !strings.Contains(h.stdout.String(), "s3cache save") {
		t.Fatalf("help output missing usage:\n%s", h.stdout.String())
	}
}

func TestPathSpellingDoesNotChangeTheCache(t *testing.T) {
	h := newHarness(t)
	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")

	h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps)

	trailing := []string{"restore", "--s3", "s3://bucket/ci", "--key", "k1", "--path", deps + "/", "--lookup-only"}
	if code, stdout := h.run(trailing...); code != 0 || stdout != "k1" {
		t.Fatalf("trailing slash = (%d, %q), want (0, %q)", code, stdout, "k1")
	}

	t.Chdir(filepath.Dir(deps))
	relative := []string{"restore", "--s3", "s3://bucket/ci", "--key", "k1", "--path", filepath.Base(deps), "--lookup-only"}
	if code, stdout := h.run(relative...); code != 0 || stdout != "k1" {
		t.Fatalf("relative path = (%d, %q), want (0, %q)", code, stdout, "k1")
	}
}

func TestKeyTemplates(t *testing.T) {
	h := newHarness(t)
	t.Setenv("CACHE_NAMESPACE", "ci")

	workspace := t.TempDir()
	lock := filepath.Join(workspace, "Cargo.lock")
	writeTestFile(t, lock, "lock-v1")
	deps := filepath.Join(workspace, "target")
	writeTestFile(t, filepath.Join(deps, "artifact"), "built")

	t.Chdir(workspace)
	template := `linux-{{ env "CACHE_NAMESPACE" }}-{{ hashFiles "Cargo.lock" }}`

	got := h.mustRun("save", "--s3", "s3://bucket/ci", "--key", template, "--path", deps)
	if !strings.HasPrefix(got, "linux-ci-") || strings.Contains(got, "${{") {
		t.Fatalf("save printed %q, want the expanded key", got)
	}

	if err := os.RemoveAll(deps); err != nil {
		t.Fatal(err)
	}
	restore := []string{"restore", "--s3", "s3://bucket/ci", "--key", template, "--path", deps}
	if matched := h.mustRun(restore...); matched != got {
		t.Fatalf("restore matched %q, want %q", matched, got)
	}
	if content := readTestFile(t, filepath.Join(deps, "artifact")); content != "built" {
		t.Fatalf("restored content = %q", content)
	}

	writeTestFile(t, lock, "lock-v2")
	if code, stdout := h.run(restore...); code != 0 || stdout != "" {
		t.Fatalf("a changed lockfile must miss: (%d, %q)", code, stdout)
	}
}

func TestKeyTemplateErrors(t *testing.T) {
	tests := map[string]string{
		"unset env":             "linux-${{ env.S3CACHE_UNSET_KEY_VAR }}",
		"unknown":               "linux-${{ runner.os }}",
		"no match":              "${{ hashFiles('definitely-missing.lock') }}",
		"empty after expansion": "${{ env.S3CACHE_EMPTY_KEY_VAR }}",
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			t.Setenv("S3CACHE_EMPTY_KEY_VAR", "  ")
			code, stdout := h.run("save", "--s3", "s3://bucket/ci", "--key", key, "--path", t.TempDir())
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
	t.Setenv("CACHE_BRANCH", "main")

	deps := t.TempDir()
	writeTestFile(t, filepath.Join(deps, "a.txt"), "a")
	h.mustRun("save", "--s3", "s3://bucket/ci", "--key", "main-linux-old", "--path", deps)

	restore := []string{"restore", "--s3", "s3://bucket/ci", "--key", "main-linux-new",
		"--restore-key", `{{ env "CACHE_BRANCH" }}-linux-`, "--path", deps, "--lookup-only"}
	if matched := h.mustRun(restore...); matched != "main-linux-old" {
		t.Fatalf("restore matched %q, want main-linux-old", matched)
	}
}
