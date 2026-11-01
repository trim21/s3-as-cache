package store

import "testing"

func TestParseURL(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantBucket string
		wantPrefix string
		wantErr    bool
	}{
		{name: "bucket and prefix", raw: "s3://build-cache/ci/github", wantBucket: "build-cache", wantPrefix: "ci/github"},
		{name: "bucket only", raw: "s3://build-cache", wantBucket: "build-cache"},
		{name: "trailing slash", raw: "s3://build-cache/ci/", wantBucket: "build-cache", wantPrefix: "ci"},
		{name: "empty", raw: "", wantErr: true},
		{name: "wrong scheme", raw: "gs://build-cache/ci", wantErr: true},
		{name: "no bucket", raw: "s3:///ci", wantErr: true},
		{name: "query", raw: "s3://build-cache/ci?region=x", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got %+v", tt.raw, cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Bucket != tt.wantBucket || cfg.Prefix != tt.wantPrefix {
				t.Fatalf("got bucket %q prefix %q, want %q and %q", cfg.Bucket, cfg.Prefix, tt.wantBucket, tt.wantPrefix)
			}
		})
	}
}
