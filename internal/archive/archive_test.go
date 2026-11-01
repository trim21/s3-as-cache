package archive

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var allCompressions = []Compression{CompressionZstd, CompressionGzip, CompressionNone}

// extractedPath maps an archived absolute path to where it lands under root.
func extractedPath(root, src string) string {
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(filepath.ToSlash(src), "/")))
}

func writeTestFile(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestCreateExtractRoundTrip(t *testing.T) {
	for _, compression := range allCompressions {
		t.Run(string(compression), func(t *testing.T) {
			src := t.TempDir()
			writeTestFile(t, filepath.Join(src, "deps", "a.txt"), "hello", 0o644)
			writeTestFile(t, filepath.Join(src, "deps", "sub", "b.bin"), "world", 0o755)
			if err := os.MkdirAll(filepath.Join(src, "empty"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("deps/a.txt", filepath.Join(src, "link")); err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			stats, err := Create(&buf, []string{src}, compression)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if stats.Files != 2 || stats.Bytes != int64(len("hello")+len("world")) {
				t.Fatalf("unexpected stats %+v", stats)
			}

			root := t.TempDir()
			extracted, err := Extract(&buf, compression, root)
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if extracted.Files != stats.Files || extracted.Bytes != stats.Bytes {
				t.Fatalf("extracted %+v, want %+v", extracted, stats)
			}

			content, err := os.ReadFile(extractedPath(root, filepath.Join(src, "deps", "a.txt")))
			if err != nil || string(content) != "hello" {
				t.Fatalf("a.txt = %q, %v", content, err)
			}

			info, err := os.Stat(extractedPath(root, filepath.Join(src, "deps", "sub", "b.bin")))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o755 {
				t.Fatalf("b.bin mode = %o, want 755", info.Mode().Perm())
			}

			dirInfo, err := os.Stat(extractedPath(root, filepath.Join(src, "empty")))
			if err != nil {
				t.Fatal(err)
			}
			if !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o750 {
				t.Fatalf("empty dir = %v mode %o", dirInfo.IsDir(), dirInfo.Mode().Perm())
			}

			link := extractedPath(root, filepath.Join(src, "link"))
			if target, err := os.Readlink(link); err != nil || target != "deps/a.txt" {
				t.Fatalf("symlink = %q, %v", target, err)
			}
		})
	}
}

func TestCreateHonoursExcludes(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "keep.txt"), "keep", 0o644)
	writeTestFile(t, filepath.Join(src, "skip", "drop.txt"), "drop", 0o644)
	writeTestFile(t, filepath.Join(src, "skip", "nested", "drop.txt"), "drop", 0o644)

	var buf bytes.Buffer
	stats, err := Create(&buf, []string{src, "!" + filepath.Join(src, "skip")}, CompressionNone)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if stats.Files != 1 {
		t.Fatalf("archived %d files, want only keep.txt", stats.Files)
	}
}

func TestCreateExpandsGlobs(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "a.txt"), "a", 0o644)
	writeTestFile(t, filepath.Join(src, "nested", "b.txt"), "b", 0o644)
	writeTestFile(t, filepath.Join(src, "nested", "c.log"), "c", 0o644)

	var buf bytes.Buffer
	stats, err := Create(&buf, []string{filepath.Join(src, "**", "*.txt")}, CompressionNone)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if stats.Files != 2 {
		t.Fatalf("archived %d files, want 2", stats.Files)
	}
}

func TestCreateDedupesNestedPaths(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "nested", "a.txt"), "a", 0o644)

	var buf bytes.Buffer
	stats, err := Create(&buf, []string{src, filepath.Join(src, "nested")}, CompressionNone)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if stats.Files != 1 {
		t.Fatalf("archived %d files, want 1", stats.Files)
	}
}

func TestCreateWithoutMatchesReportsNoFiles(t *testing.T) {
	tests := map[string][]string{
		"missing path": {filepath.Join(t.TempDir(), "does-not-exist")},
		"empty dir":    {t.TempDir()},
	}
	for name, patterns := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if _, err := Create(&buf, patterns, CompressionNone); !errors.Is(err, ErrNoFiles) {
				t.Fatalf("err = %v, want ErrNoFiles", err)
			}
		})
	}
}

func TestExtractKeepsPathsInsideRoot(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("escaped")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "../escape.txt",
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if _, err := Extract(&buf, CompressionNone, root); err != nil {
		t.Fatalf("extract: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "escape.txt")); err != nil {
		t.Fatalf("expected the entry to land inside root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Fatal("entry escaped the extraction root")
	}
}

func TestNormalizePatterns(t *testing.T) {
	got := NormalizePatterns([]string{" ~/.cargo/ ", "", "/tmp/cache/", "!/tmp/cache/skip/", "relative"})
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		filepath.Join(home, ".cargo"),
		"/tmp/cache",
		"!/tmp/cache/skip",
		filepath.Join(cwd, "relative"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}
