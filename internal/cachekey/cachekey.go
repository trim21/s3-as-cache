// Package cachekey derives where a cache archive lives and which stored archive
// a lookup should pick.
package cachekey

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/trim21/s3-as-cache/internal/store"
)

// Version fingerprints the path set, compression and platform an archive was
// built for. Archives under different versions never match each other, so a
// changed path list cannot restore stale content.
func Version(paths []string, compression, platform string) string {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		normalized = append(normalized, p)
	}
	sort.Strings(normalized)

	h := sha256.New()
	for _, p := range normalized {
		fmt.Fprintf(h, "%s\x00", p)
	}
	fmt.Fprintf(h, "%s\x00%s", compression, platform)

	return hex.EncodeToString(h.Sum(nil))[:32]
}

// ObjectPrefix is the listing prefix that holds every archive of one version.
func ObjectPrefix(prefix, version string) string {
	if prefix == "" {
		return version + "/"
	}
	return strings.Trim(prefix, "/") + "/" + version + "/"
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
