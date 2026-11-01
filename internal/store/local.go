package store

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Local keeps archives in a directory. It mirrors the S3 layout so the cache
// logic can be exercised without a bucket.
type Local struct {
	Root string
}

func (l *Local) path(key string) string {
	return filepath.Join(l.Root, filepath.FromSlash(key))
}

func (l *Local) List(_ context.Context, prefix string) ([]Object, error) {
	root := l.Root
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	var objects []Object
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		objects = append(objects, Object{Key: key, Size: info.Size(), LastModified: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return objects, nil
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(l.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (l *Local) Put(_ context.Context, key string, body io.Reader, _ int64, ifAbsent bool) error {
	p := l.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}

	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if ifAbsent {
		flags = os.O_CREATE | os.O_WRONLY | os.O_EXCL
	}
	f, err := os.OpenFile(p, flags, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return ErrAlreadyExists
	}
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		os.Remove(p)
		return err
	}
	return f.Close()
}
