package tmpl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExpandLiteral(t *testing.T) {
	for _, input := range []string{"linux-amd64-v1", "a$b{c}d", "with space"} {
		got, err := Expand(input)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", input, err)
		}
		if got != input {
			t.Fatalf("got %q, want %q", got, input)
		}
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("CACHE_NS", "feature-x")

	got, err := Expand(`linux-{{ env "CACHE_NS" }}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "linux-feature-x"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEnvUnset(t *testing.T) {
	_, err := Expand(`{{ env "S3CACHE_TEST_UNSET_VARIABLE" }}`)
	if err == nil {
		t.Fatal("expected an error for an unset variable")
	}
	if !strings.Contains(err.Error(), "S3CACHE_TEST_UNSET_VARIABLE") {
		t.Fatalf("error should name the variable: %v", err)
	}
}

func TestExpandSupportsTemplateSyntax(t *testing.T) {
	t.Setenv("CACHE_NS", "Main")

	got, err := Expand(`{{ printf "%s-%s" (env "CACHE_NS") "x" }}{{ if env "CACHE_NS" }}cached{{ end }}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "Main-xcached"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandHashFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, "Cargo.lock", "lock-v1")
	writeFile(t, filepath.Join("nested", "package-lock.json"), `{"version":1}`)

	first, err := Expand(`{{ hashFiles "Cargo.lock" "nested/*.json" }}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("hash = %q, want a 64 character sha256", first)
	}

	again, err := Expand(`{{ hashFiles "Cargo.lock" "nested/*.json" }}`)
	if err != nil || again != first {
		t.Fatalf("hash is not stable: %q != %q (%v)", again, first, err)
	}

	// Argument order must not change the fingerprint.
	reordered, err := Expand(`{{ hashFiles "nested/*.json" "Cargo.lock" }}`)
	if err != nil || reordered != first {
		t.Fatalf("argument order changed the hash: %q != %q (%v)", reordered, first, err)
	}

	// A subset of files must not hash like the whole set.
	partial, err := Expand(`{{ hashFiles "Cargo.lock" }}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if partial == first {
		t.Fatal("hashing a subset produced the same fingerprint")
	}

	writeFile(t, "Cargo.lock", "lock-v2")
	changed, err := Expand(`{{ hashFiles "Cargo.lock" "nested/*.json" }}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed == first {
		t.Fatal("content change did not change the fingerprint")
	}
}

func TestExpandHashFilesIsIndependentOfWorkspaceLocation(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "checkout-a", "proj")
	second := filepath.Join(base, "checkout-b", "proj")
	writeFile(t, filepath.Join(first, "Cargo.lock"), "lock")
	writeFile(t, filepath.Join(second, "Cargo.lock"), "lock")

	hashIn := func(dir string) string {
		t.Helper()
		t.Chdir(dir)
		hash, err := Expand(`{{ hashFiles "Cargo.lock" }}`)
		if err != nil {
			t.Fatalf("hash in %s: %v", dir, err)
		}
		return hash
	}

	if a, b := hashIn(first), hashIn(second); a != b {
		t.Fatalf("the same checkout content hashed differently per location: %q != %q", a, b)
	}
}

func TestExpandErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	tests := map[string]string{
		"unset env":        `{{ env "S3CACHE_TEST_UNSET_VARIABLE" }}`,
		"env without arg":  `{{ env }}`,
		"unknown function": `{{ github "sha" }}`,
		"bad syntax":       `linux-{{ env "A" `,
		"no matches":       `{{ hashFiles "does-not-exist.lock" }}`,
		"no arguments":     `{{ hashFiles }}`,
		"empty pattern":    `{{ hashFiles "" }}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := Expand(input); err == nil {
				t.Fatalf("expected an error, got %q", got)
			}
		})
	}
}
