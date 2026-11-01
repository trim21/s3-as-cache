package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// integrationConfig skips the test unless a real S3 endpoint is configured.
// CI points this at a Silo instance; see .github/workflows/ci.yml.
func integrationConfig(t *testing.T) S3Config {
	t.Helper()

	raw := os.Getenv("S3CACHE_TEST_S3")
	endpoint := os.Getenv("S3CACHE_TEST_ENDPOINT")
	if raw == "" || endpoint == "" {
		t.Skip("set S3CACHE_TEST_S3 and S3CACHE_TEST_ENDPOINT to run the S3 integration tests")
	}

	cfg, err := ParseURL(raw)
	if err != nil {
		t.Fatalf("parse S3CACHE_TEST_S3: %v", err)
	}
	cfg.Endpoint = endpoint
	cfg.ForcePathStyle = true
	return cfg
}

func TestS3StoreIntegration(t *testing.T) {
	cfg := integrationConfig(t)
	ctx := context.Background()

	st, err := NewS3(ctx, cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	prefix := fmt.Sprintf("%s/integration/%d/", cfg.Prefix, time.Now().UnixNano())
	key := prefix + "entry.tar.zst"
	t.Cleanup(func() { removePrefix(t, st, prefix) })

	if objects, err := st.List(ctx, prefix); err != nil {
		t.Fatalf("list empty prefix: %v", err)
	} else if len(objects) != 0 {
		t.Fatalf("expected an empty prefix, got %d object(s)", len(objects))
	}

	if err := st.Put(ctx, key, strings.NewReader("first"), int64(len("first")), true); err != nil {
		t.Fatalf("put: %v", err)
	}

	if err := st.Put(ctx, key, strings.NewReader("second"), int64(len("second")), true); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("conditional put over an existing object = %v, want ErrAlreadyExists", err)
	}

	if err := st.Put(ctx, key, strings.NewReader("second"), int64(len("second")), false); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	objects, err := st.List(ctx, prefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objects) != 1 || objects[0].Key != key {
		t.Fatalf("list = %+v, want the single key %s", objects, key)
	}
	if objects[0].Size != int64(len("second")) {
		t.Fatalf("size = %d, want %d", objects[0].Size, len("second"))
	}
	if objects[0].LastModified.IsZero() {
		t.Fatal("list must report a modification time")
	}

	body, err := st.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	content, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(content) != "second" {
		t.Fatalf("get = %q, %v", content, err)
	}

	if _, err := st.Get(ctx, prefix+"absent.tar.zst"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing object = %v, want ErrNotFound", err)
	}
}

func removePrefix(t *testing.T, st *S3, prefix string) {
	t.Helper()

	ctx := context.Background()
	objects, err := st.List(ctx, prefix)
	if err != nil {
		t.Logf("cleanup: list %s: %v", prefix, err)
		return
	}
	for _, obj := range objects {
		_, err := st.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(st.bucket),
			Key:    aws.String(obj.Key),
		})
		if err != nil {
			t.Logf("cleanup: delete %s: %v", obj.Key, err)
		}
	}
}
