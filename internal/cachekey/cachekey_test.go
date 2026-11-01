package cachekey

import (
	"testing"
	"time"

	"github.com/trim21/s3-as-cache/internal/store"
)

func TestVersionIsStableAcrossPathOrder(t *testing.T) {
	got := Version([]string{"target", "~/.cargo"}, "zstd", "linux-amd64")
	want := Version([]string{"~/.cargo", "target", " target "}, "zstd", "linux-amd64")
	if got != want {
		t.Fatalf("version depends on path order or duplicates: %s != %s", got, want)
	}
	if len(got) != 32 {
		t.Fatalf("expected a 32 character version, got %q", got)
	}
}

func TestVersionSeparatesCompressionAndPlatform(t *testing.T) {
	base := Version([]string{"target"}, "zstd", "linux-amd64")
	cases := map[string]string{
		"other compression": Version([]string{"target"}, "gzip", "linux-amd64"),
		"other platform":    Version([]string{"target"}, "zstd", "darwin-arm64"),
		"cross platform":    Version([]string{"target"}, "zstd", ""),
		"other paths":       Version([]string{"target", "extra"}, "zstd", "linux-amd64"),
	}
	for name, other := range cases {
		if other == base {
			t.Errorf("%s must produce a different version", name)
		}
	}
}

func TestObjectPrefix(t *testing.T) {
	tests := []struct {
		prefix  string
		version string
		want    string
	}{
		{"", "v1", "v1/"},
		{"cache", "v1", "cache/v1/"},
		{"/cache/ci/", "v1", "cache/ci/v1/"},
	}
	for _, tt := range tests {
		if got := ObjectPrefix(tt.prefix, tt.version); got != tt.want {
			t.Errorf("ObjectPrefix(%q, %q) = %q, want %q", tt.prefix, tt.version, got, tt.want)
		}
	}
}

func TestKeyFromObject(t *testing.T) {
	const prefix = "cache/v1/"

	tests := []struct {
		objectKey string
		want      string
		wantOK    bool
	}{
		{"cache/v1/main-linux-abc.tar.zst", "main-linux-abc", true},
		{"cache/v1/nested/main-abc.tar.zst", "nested/main-abc", true},
		{"cache/v1/main-abc.tar.gz", "", false},
		{"cache/v2/main-abc.tar.zst", "", false},
		{"cache/v1/.tar.zst", "", false},
	}
	for _, tt := range tests {
		got, ok := KeyFromObject(tt.objectKey, prefix, ".tar.zst")
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("KeyFromObject(%q) = (%q, %t), want (%q, %t)", tt.objectKey, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestMatch(t *testing.T) {
	now := time.Now()
	objects := []store.Object{
		{Key: "linux-abc", LastModified: now.Add(-2 * time.Hour)},
		{Key: "linux-def", LastModified: now.Add(-1 * time.Hour)},
		{Key: "macos-abc", LastModified: now.Add(-3 * time.Hour)},
	}

	tests := []struct {
		name        string
		primary     string
		restoreKeys []string
		want        string
		wantOK      bool
	}{
		{"exact match ignores newer prefixes", "linux-abc", []string{"linux-"}, "linux-abc", true},
		{"prefix match picks the newest", "linux-zzz", []string{"linux-"}, "linux-def", true},
		{"restore keys are tried in order", "missing", []string{"macos-", "linux-"}, "macos-abc", true},
		{"no match", "missing", []string{"windows-", " "}, "", false},
		{"no restore keys", "missing", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Match(objects, tt.primary, tt.restoreKeys)
			if ok != tt.wantOK {
				t.Fatalf("match = %t, want %t", ok, tt.wantOK)
			}
			if ok && got.Key != tt.want {
				t.Fatalf("matched %q, want %q", got.Key, tt.want)
			}
		})
	}
}

func TestMatchBreaksTiesDeterministically(t *testing.T) {
	same := time.Now()
	objects := []store.Object{
		{Key: "linux-b", LastModified: same},
		{Key: "linux-a", LastModified: same},
	}
	got, ok := Match(objects, "linux-z", []string{"linux-"})
	if !ok || got.Key != "linux-b" {
		t.Fatalf("match = (%q, %t), want the lexicographically greatest key", got.Key, ok)
	}
}
