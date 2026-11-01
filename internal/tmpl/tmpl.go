// Package tmpl renders cache keys as Go text/templates, so a key can be derived
// from file contents or the environment instead of being hardcoded.
package tmpl

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/bmatcuk/doublestar/v4"
)

// Expand renders input as a text/template. Keys without actions are returned
// unchanged; see FuncMap for the functions available to templates.
func Expand(input string) (string, error) {
	parsed, err := template.New("key").Funcs(FuncMap()).Parse(input)
	if err != nil {
		return "", fmt.Errorf("parse cache key template: %w", err)
	}

	var out strings.Builder
	if err := parsed.Execute(&out, nil); err != nil {
		return "", fmt.Errorf("evaluate cache key template: %w", err)
	}
	return out.String(), nil
}

// FuncMap is the function set available in key templates.
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"env":       lookupEnv,
		"hashFiles": hashFiles,
	}
}

// lookupEnv returns an environment variable. An unset variable is an error
// rather than an empty string, so a missing value cannot silently collapse
// different environments into one cache key.
func lookupEnv(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("env needs a variable name")
	}
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return value, nil
}

// hashFiles fingerprints the matched files by content. Paths are hashed relative
// to the working directory, so the same checkout in a different directory keys
// the same cache.
func hashFiles(patterns ...string) (string, error) {
	if len(patterns) == 0 {
		return "", errors.New("hashFiles needs at least one glob pattern")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	seen := make(map[string]struct{})
	var files []string
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" {
			return "", errors.New("hashFiles glob patterns must not be empty")
		}
		abs, err := filepath.Abs(pattern)
		if err != nil {
			return "", err
		}
		matches, err := doublestar.FilepathGlob(abs)
		if err != nil {
			return "", fmt.Errorf("expand hashFiles pattern %q: %w", pattern, err)
		}
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				return "", err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			files = append(files, match)
		}
	}
	if len(files) == 0 {
		return "", fmt.Errorf("hashFiles matched no files for %s", strings.Join(patterns, ", "))
	}
	sort.Strings(files)

	h := sha256.New()
	for _, file := range files {
		sum, err := fileSum(file)
		if err != nil {
			return "", err
		}
		name, err := filepath.Rel(cwd, file)
		if err != nil {
			name = file
		}
		fmt.Fprintf(h, "%s\x00%x\n", filepath.ToSlash(name), sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSum(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
