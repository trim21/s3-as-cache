package archive

import (
	"compress/gzip"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// Compression is the archive compression method.
type Compression string

const (
	CompressionZstd Compression = "zstd"
	CompressionGzip Compression = "gzip"
	CompressionNone Compression = "none"
)

// ParseCompression validates a --compression value.
func ParseCompression(s string) (Compression, error) {
	switch Compression(s) {
	case CompressionZstd, CompressionGzip, CompressionNone:
		return Compression(s), nil
	default:
		return "", fmt.Errorf("unknown compression %q, expected zstd, gzip or none", s)
	}
}

// Extension is the file suffix archives of this compression use in the bucket.
func (c Compression) Extension() string {
	switch c {
	case CompressionGzip:
		return ".tar.gz"
	case CompressionNone:
		return ".tar"
	default:
		return ".tar.zst"
	}
}

func (c Compression) newWriter(w io.Writer) (io.WriteCloser, error) {
	switch c {
	case CompressionGzip:
		return gzip.NewWriter(w), nil
	case CompressionNone:
		return nopWriteCloser{w}, nil
	case CompressionZstd:
		return zstd.NewWriter(w)
	default:
		return nil, fmt.Errorf("unknown compression %q", c)
	}
}

func (c Compression) newReader(r io.Reader) (io.ReadCloser, error) {
	switch c {
	case CompressionGzip:
		return gzip.NewReader(r)
	case CompressionNone:
		return io.NopCloser(r), nil
	case CompressionZstd:
		decoder, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return zstdReadCloser{decoder}, nil
	default:
		return nil, fmt.Errorf("unknown compression %q", c)
	}
}

// zstdReadCloser adapts the decoder, whose Close reports no error.
type zstdReadCloser struct {
	*zstd.Decoder
}

func (z zstdReadCloser) Close() error {
	z.Decoder.Close()
	return nil
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
