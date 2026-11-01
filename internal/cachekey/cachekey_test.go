package cachekey

import (
	"testing"
	"time"

	"github.com/trim21/s3-as-cache/internal/store"
)

func TestObjectPrefix(t *testing.T) {
	tests := []struct {
		prefix   string
		platform string
		want     string
	}{
		{"", "", ""},
		{"cache", "", "cache/"},
		{"", "linux-amd64", "linux-amd64/"},
		{"cache", "linux-amd64", "cache/linux-amd64/"},
		{"/cache/ci/", "darwin-arm64", "cache/ci/darwin-arm64/"},
	}
	for _, tt := range tests {
		if got := ObjectPrefix(tt.prefix, tt.platform); got != tt.want {
			t.Errorf("ObjectPrefix(%q, %q) = %q, want %q", tt.prefix, tt.platform, got, tt.want)
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
