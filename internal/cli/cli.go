// Package cli implements the s3cache command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/trim21/s3-as-cache/internal/archive"
	"github.com/trim21/s3-as-cache/internal/cachekey"
	"github.com/trim21/s3-as-cache/internal/store"
	"github.com/trim21/s3-as-cache/internal/tmpl"
)

const usageText = `s3cache keeps build caches in an S3 bucket, for any CI that can run a binary.

usage:
  s3cache save    --s3 s3://bucket/prefix --key KEY --path PATH [--path PATH]...
  s3cache restore --s3 s3://bucket/prefix --key KEY [--restore-key PREFIX]... --path PATH [--path PATH]...

Both commands print the cache key they settled on to stdout and log progress to
stderr.

--key and --restore-key are Go text templates:
  {{ env "NAME" }}           an environment variable, which must be set
  {{ hashFiles "glob" ... }}  sha256 over the matched file contents

Credentials come from the standard AWS chain (AWS_ACCESS_KEY_ID and friends,
shared config, container or instance profile).

run "s3cache save -h" or "s3cache restore -h" for the full flag list.
`

// Options carries the process boundaries so the CLI can be driven from tests.
type Options struct {
	Stdout io.Writer
	Stderr io.Writer
	// OpenStore overrides how the object store is built. Nil means S3.
	OpenStore func(context.Context, store.S3Config) (store.Store, error)
}

// Run executes a command line and returns the process exit code.
func Run(ctx context.Context, args []string, opts Options) int {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	if len(args) == 0 {
		fmt.Fprint(opts.Stderr, usageText)
		return 2
	}

	switch args[0] {
	case "save":
		return runSave(ctx, args[1:], opts)
	case "restore":
		return runRestore(ctx, args[1:], opts)
	case "help", "-h", "--help":
		fmt.Fprint(opts.Stdout, usageText)
		return 0
	default:
		fmt.Fprintf(opts.Stderr, "unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

type commonFlags struct {
	s3URL          string
	region         string
	endpoint       string
	forcePathStyle bool
	compression    string
	crossPlatform  bool
	quiet          bool
	verbose        bool
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.s3URL, "s3", "", "bucket and key prefix holding the cache, as s3://bucket/prefix (required)")
	fs.StringVar(&c.region, "region", "", "AWS region, defaults to the AWS SDK configuration")
	fs.StringVar(&c.endpoint, "endpoint-url", "", "S3-compatible endpoint to use instead of AWS")
	fs.BoolVar(&c.forcePathStyle, "force-path-style", false, "address the bucket as path style instead of virtual host style")
	fs.StringVar(&c.compression, "compression", string(archive.CompressionZstd), "archive compression: zstd, gzip or none")
	fs.BoolVar(&c.crossPlatform, "cross-platform", false, "let archives match across operating systems and architectures")
	fs.BoolVar(&c.quiet, "quiet", false, "log only errors")
	fs.BoolVar(&c.verbose, "verbose", false, "log lookup and archive details")
}

// resolved holds everything the flags determine about the storage location,
// before any network call.
type resolved struct {
	cfg    store.S3Config
	comp   archive.Compression
	prefix string
}

func (c *commonFlags) resolve(paths []string) (resolved, error) {
	comp, err := archive.ParseCompression(c.compression)
	if err != nil {
		return resolved{}, err
	}

	cfg, err := store.ParseURL(c.s3URL)
	if err != nil {
		return resolved{}, err
	}
	cfg.Region = c.region
	cfg.Endpoint = c.endpoint
	cfg.ForcePathStyle = c.forcePathStyle

	platform := ""
	if !c.crossPlatform {
		platform = runtime.GOOS + "-" + runtime.GOARCH
	}

	return resolved{
		cfg:    cfg,
		comp:   comp,
		prefix: cachekey.ObjectPrefix(cfg.Prefix, cachekey.Version(archive.NormalizePatterns(paths), string(comp), platform)),
	}, nil
}

// session is the store plus everything a command needs to do its work.
type session struct {
	store  store.Store
	log    *logger
	comp   archive.Compression
	prefix string
}

func openSession(ctx context.Context, opts Options, r resolved, log *logger) (*session, error) {
	openStore := opts.OpenStore
	if openStore == nil {
		openStore = func(ctx context.Context, cfg store.S3Config) (store.Store, error) {
			return store.NewS3(ctx, cfg)
		}
	}

	st, err := openStore(ctx, r.cfg)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	return &session{store: st, log: log, comp: r.comp, prefix: r.prefix}, nil
}

type logger struct {
	w       io.Writer
	quiet   bool
	verbose bool
}

func newLogger(w io.Writer, quiet, verbose bool) *logger {
	return &logger{w: w, quiet: quiet, verbose: verbose}
}

func (l *logger) infof(format string, args ...any) {
	if l.quiet {
		return
	}
	fmt.Fprintf(l.w, format+"\n", args...)
}

func (l *logger) debugf(format string, args ...any) {
	if !l.verbose {
		return
	}
	fmt.Fprintf(l.w, "debug: "+format+"\n", args...)
}

func (l *logger) errorf(format string, args ...any) {
	fmt.Fprintf(l.w, format+"\n", args...)
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ", ")
}

func (m *multiFlag) Set(value string) error {
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			*m = append(*m, line)
		}
	}
	return nil
}

// expandKeys applies the key template syntax to the primary key and every
// restore key.
func expandKeys(values []string) ([]string, error) {
	out := make([]string, len(values))
	for i, value := range values {
		expanded, err := tmpl.Expand(value)
		if err != nil {
			return nil, err
		}
		out[i] = expanded
	}
	return out, nil
}

func validateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("--key must not be empty")
	}
	if strings.ContainsAny(key, "\x00\n\r") {
		return errors.New("--key must not contain control characters")
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
