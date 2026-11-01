package archive

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var allCompressions = []Compression{CompressionZstd, CompressionGzip, CompressionNone}

func testManifest(paths ...string) Manifest {
	return Manifest{
		Paths:     paths,
		Platform:  "linux-amd64",
		CreatedAt: time.Now().UTC(),
	}
}

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
			stats, err := Create(&buf, []string{src}, compression, testManifest(src))
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if stats.Files != 2 || stats.Bytes != int64(len("hello")+len("world")) {
				t.Fatalf("unexpected stats %+v", stats)
			}

			root := t.TempDir()
			extracted, manifest, err := Extract(&buf, compression, root)
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if len(manifest.Paths) != 1 || manifest.Paths[0] != src {
				t.Fatalf("manifest paths = %q, want %q", manifest.Paths, src)
			}
			if manifest.Compression != string(compression) {
				t.Fatalf("manifest compression = %q", manifest.Compression)
			}
			if _, err := os.Stat(extractedPath(root, "/"+manifestName)); err == nil {
				t.Fatal("the manifest must not be extracted onto the filesystem")
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
	stats, err := Create(&buf, []string{src, "!" + filepath.Join(src, "skip")}, CompressionNone, testManifest(src))
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
	stats, err := Create(&buf, []string{filepath.Join(src, "**", "*.txt")}, CompressionNone, testManifest(src))
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
	stats, err := Create(&buf, []string{src, filepath.Join(src, "nested")}, CompressionNone, testManifest(src))
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
			if _, err := Create(&buf, patterns, CompressionNone, testManifest(patterns...)); !errors.Is(err, ErrNoFiles) {
				t.Fatalf("err = %v, want ErrNoFiles", err)
			}
		})
	}
}

func TestExtractKeepsPathsInsideRoot(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeManifest(tw, testManifest("/tmp/escape.txt")); err != nil {
		t.Fatal(err)
	}
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
	if _, _, err := Extract(&buf, CompressionNone, root); err != nil {
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

func TestManifestDoesNotCollideWithCachedFiles(t *testing.T) {
	src := t.TempDir()
	nested := filepath.Join(src, "nested", ".s3cache.json")
	writeTestFile(t, nested, `{"note":"a real cached file"}`, 0o644)

	var buf bytes.Buffer
	if _, err := Create(&buf, []string{src}, CompressionNone, testManifest(src)); err != nil {
		t.Fatalf("create: %v", err)
	}

	root := t.TempDir()
	stats, manifest, err := Extract(&buf, CompressionNone, root)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if stats.Files != 1 {
		t.Fatalf("extracted %d file(s), want the cached .s3cache.json to be treated as data", stats.Files)
	}
	if len(manifest.Paths) != 1 || manifest.Paths[0] != src {
		t.Fatalf("manifest paths = %q, want %q", manifest.Paths, src)
	}

	content, err := os.ReadFile(extractedPath(root, nested))
	if err != nil {
		t.Fatalf("the cached .s3cache.json was not restored: %v", err)
	}
	if string(content) != `{"note":"a real cached file"}` {
		t.Fatalf("restored content = %q", content)
	}
}

func TestExtractRequiresTheManifestFirst(t *testing.T) {
	t.Run("no manifest", func(t *testing.T) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		body := []byte("plain")
		if err := tw.WriteHeader(&tar.Header{
			Name:     "tmp/plain.txt",
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

		if _, _, err := Extract(&buf, CompressionNone, t.TempDir()); !errors.Is(err, ErrMissingManifest) {
			t.Fatalf("err = %v, want ErrMissingManifest", err)
		}
	})

	t.Run("empty archive", func(t *testing.T) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}

		if _, _, err := Extract(&buf, CompressionNone, t.TempDir()); !errors.Is(err, ErrMissingManifest) {
			t.Fatalf("err = %v, want ErrMissingManifest", err)
		}
	})
}
