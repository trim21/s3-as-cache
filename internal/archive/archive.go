// Package archive packs cache paths into a compressed tar stream and unpacks it
// back onto the filesystem.
package archive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ErrNoFiles means no path pattern matched anything, so there is nothing worth
// storing.
var ErrNoFiles = errors.New("no files matched cache paths")

// Stats summarises an archive operation.
type Stats struct {
	Files int
	Bytes int64
}

type entry struct {
	path string
	name string
	info fs.FileInfo
	link string
}

// Create writes a compressed tar stream of patterns to w. Patterns are files,
// directories or globs, resolved relative to the working directory; a pattern
// prefixed with "!" excludes matching paths.
func Create(w io.Writer, patterns []string, compression Compression) (Stats, error) {
	entries, err := collect(patterns)
	if err != nil {
		return Stats{}, err
	}

	var stats Stats
	for _, e := range entries {
		if e.info.Mode().IsRegular() {
			stats.Files++
			stats.Bytes += e.info.Size()
		}
	}
	if stats.Files == 0 {
		return Stats{}, ErrNoFiles
	}

	zw, err := compression.newWriter(w)
	if err != nil {
		return Stats{}, err
	}
	tw := tar.NewWriter(zw)

	for _, e := range entries {
		header, err := tar.FileInfoHeader(e.info, e.link)
		if err != nil {
			return Stats{}, fmt.Errorf("describe %s: %w", e.path, err)
		}
		header.Name = e.name
		if err := tw.WriteHeader(header); err != nil {
			return Stats{}, err
		}
		if !e.info.Mode().IsRegular() {
			continue
		}
		if err := writeFile(tw, e.path); err != nil {
			return Stats{}, err
		}
	}

	if err := tw.Close(); err != nil {
		return Stats{}, err
	}
	if err := zw.Close(); err != nil {
		return Stats{}, err
	}

	return stats, nil
}

func writeFile(w io.Writer, name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(w, f)
	return err
}

// Extract unpacks a compressed tar stream into root. Entries are stored relative
// to the filesystem root, so a normal restore uses "/".
func Extract(r io.Reader, compression Compression, root string) (Stats, error) {
	zr, err := compression.newReader(r)
	if err != nil {
		return Stats{}, err
	}
	defer zr.Close()

	var stats Stats
	type dirMode struct {
		path string
		mode fs.FileMode
	}
	var dirs []dirMode

	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, err
		}

		// Cleaning against the root drops any ".." segments, so extracted paths
		// can never escape root.
		name := strings.TrimPrefix(path.Clean("/"+header.Name), "/")
		if name == "" {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		mode := header.FileInfo().Mode().Perm()

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return stats, err
			}
			dirs = append(dirs, dirMode{path: target, mode: mode})
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return stats, err
			}
			// Drop whatever is there first: an existing symlink would redirect the
			// write, and a read-only file would refuse truncation.
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return stats, err
			}
			written, err := extractFile(tr, target, mode)
			if err != nil {
				return stats, err
			}
			stats.Files++
			stats.Bytes += written
			_ = os.Chtimes(target, header.ModTime, header.ModTime)
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return stats, err
			}
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return stats, err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return stats, err
			}
		}
	}

	// Directories are created writable so their contents can land, then get the
	// permissions they were archived with.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return stats, err
		}
	}

	return stats, nil
}

func extractFile(r io.Reader, target string, mode fs.FileMode) (int64, error) {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(f, r)
	if err != nil {
		f.Close()
		return written, err
	}
	return written, f.Close()
}

func collect(patterns []string) ([]entry, error) {
	roots, excludes, err := expandPatterns(patterns)
	if err != nil {
		return nil, err
	}

	var entries []entry
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if excluded(excludes, p) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return err
			}
			switch {
			case info.IsDir(), info.Mode().IsRegular():
			case info.Mode()&fs.ModeSymlink != 0:
			default:
				return nil
			}

			e := entry{path: p, name: entryName(p), info: info}
			if info.Mode()&fs.ModeSymlink != 0 {
				if e.link, err = os.Readlink(p); err != nil {
					return err
				}
			}
			entries = append(entries, e)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return entries, nil
}

// NormalizePatterns rewrites path patterns into the absolute form used both for
// archiving and for the cache version fingerprint, so "/tmp/x" and "/tmp/x/"
// describe the same cache.
func NormalizePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}

		negated := false
		if rest, ok := strings.CutPrefix(pattern, "!"); ok {
			negated = true
			if pattern = strings.TrimSpace(rest); pattern == "" {
				continue
			}
		}

		if abs, err := filepath.Abs(expandHome(pattern)); err == nil {
			pattern = abs
		}
		if negated {
			pattern = "!" + pattern
		}
		out = append(out, pattern)
	}
	return out
}

func expandPatterns(patterns []string) ([]string, []string, error) {
	var roots, excludes []string
	seen := make(map[string]struct{})

	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}

		if negated, ok := strings.CutPrefix(pattern, "!"); ok {
			negated = strings.TrimSpace(negated)
			if negated == "" {
				continue
			}
			abs, err := filepath.Abs(expandHome(negated))
			if err != nil {
				return nil, nil, err
			}
			excludes = append(excludes, abs)
			continue
		}

		abs, err := filepath.Abs(expandHome(pattern))
		if err != nil {
			return nil, nil, err
		}

		if !hasMeta(abs) {
			if _, err := os.Lstat(abs); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return nil, nil, err
			}
			if _, ok := seen[abs]; !ok {
				seen[abs] = struct{}{}
				roots = append(roots, abs)
			}
			continue
		}

		matches, err := doublestar.FilepathGlob(abs)
		if err != nil {
			return nil, nil, fmt.Errorf("expand path pattern %q: %w", raw, err)
		}
		for _, m := range matches {
			if _, ok := seen[m]; !ok {
				seen[m] = struct{}{}
				roots = append(roots, m)
			}
		}
	}

	sort.Strings(roots)
	return dropNested(roots), excludes, nil
}

// dropNested removes paths already covered by a parent path, so nothing is
// archived twice.
func dropNested(paths []string) []string {
	var out []string
	for _, p := range paths {
		nested := false
		for _, parent := range out {
			if strings.HasPrefix(p, parent+string(filepath.Separator)) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, p)
		}
	}
	return out
}

func excluded(excludes []string, p string) bool {
	target := filepath.ToSlash(p)
	for _, pattern := range excludes {
		if ok, err := doublestar.Match(filepath.ToSlash(pattern), target); err == nil && ok {
			return true
		}
	}
	return false
}

func entryName(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(p), "/")
}

func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

func hasMeta(p string) bool {
	return strings.ContainsAny(p, "*?[{")
}
