// Package cachekey derives where a cache archive lives and which stored archive
// a lookup should pick.
package cachekey

import (
	"strings"

	"github.com/trim21/s3-as-cache/internal/store"
)

// ObjectPrefix is the listing prefix that holds the caches of one platform.
// An empty platform (cross-platform caches) leaves that segment out.
func ObjectPrefix(prefix, platform string) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.Trim(prefix, "/"); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if platform != "" {
		parts = append(parts, platform)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "/") + "/"
}

// ArchiveKey is the object key suffix for a cache key.
func ArchiveKey(key, extension string) string {
	return key + extension
}

// KeyFromObject maps a stored object back to its cache key. Objects that are
// not archives of this version are rejected.
func KeyFromObject(objectKey, listPrefix, extension string) (string, bool) {
	if !strings.HasPrefix(objectKey, listPrefix) || !strings.HasSuffix(objectKey, extension) {
		return "", false
	}
	key := strings.TrimSuffix(strings.TrimPrefix(objectKey, listPrefix), extension)
	if key == "" {
		return "", false
	}
	return key, true
}

// Match picks the archive to restore: the exact primary key first, then each
// restore key as a prefix, most recently stored entry winning.
func Match(objects []store.Object, primary string, restoreKeys []string) (store.Object, bool) {
	if obj, ok := newest(objects, func(key string) bool { return key == primary }); ok {
		return obj, true
	}
	for _, raw := range restoreKeys {
		restoreKey := strings.TrimSpace(raw)
		if restoreKey == "" {
			continue
		}
		if obj, ok := newest(objects, func(key string) bool {
			return strings.HasPrefix(key, restoreKey)
		}); ok {
			return obj, true
		}
	}
	return store.Object{}, false
}

func newest(objects []store.Object, match func(key string) bool) (store.Object, bool) {
	var best store.Object
	found := false
	for _, obj := range objects {
		if !match(obj.Key) {
			continue
		}
		if !found || obj.LastModified.After(best.LastModified) ||
			(obj.LastModified.Equal(best.LastModified) && obj.Key > best.Key) {
			best = obj
			found = true
		}
	}
	return best, found
}
