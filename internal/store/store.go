// Package store abstracts the object storage that holds cache archives.
package store

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrNotFound is returned by Get when the object does not exist.
	ErrNotFound = errors.New("object not found")
	// ErrAlreadyExists is returned by Put when ifAbsent is set and the object
	// is already stored.
	ErrAlreadyExists = errors.New("object already exists")
)

// Object describes a stored archive.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Store is the minimal object storage surface the cache needs.
type Store interface {
	// List returns every object whose key starts with prefix.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Get returns the body of key.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Put stores size bytes from body under key. When ifAbsent is set the write
	// must not replace an existing object and returns ErrAlreadyExists instead.
	Put(ctx context.Context, key string, body io.Reader, size int64, ifAbsent bool) error
}
