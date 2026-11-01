package store

import (
	"fmt"
	"net/url"
	"strings"
)

// S3Config locates the bucket that holds caches.
type S3Config struct {
	Bucket string
	Prefix string

	Region         string
	Endpoint       string
	ForcePathStyle bool
}

// ParseURL turns s3://bucket/prefix into an S3Config.
func ParseURL(raw string) (S3Config, error) {
	if strings.TrimSpace(raw) == "" {
		return S3Config{}, fmt.Errorf("missing s3 location, expected s3://bucket/prefix")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return S3Config{}, fmt.Errorf("parse s3 location %q: %w", raw, err)
	}
	if u.Scheme != "s3" {
		return S3Config{}, fmt.Errorf("s3 location %q must use the s3:// scheme", raw)
	}
	if u.Host == "" {
		return S3Config{}, fmt.Errorf("s3 location %q has no bucket name", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return S3Config{}, fmt.Errorf("s3 location %q must not carry a query or fragment", raw)
	}

	return S3Config{
		Bucket: u.Host,
		Prefix: strings.Trim(u.Path, "/"),
	}, nil
}
