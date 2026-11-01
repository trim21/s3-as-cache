// Package cli implements the s3cache command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trim21/s3-as-cache/internal/store"
	"github.com/trim21/s3-as-cache/internal/tmpl"
)

const envPrefix = "S3CACHE"

const rootDescription = `s3cache keeps build caches in an S3 bucket, for any CI that can run a binary.

Every option except the path list is read from the environment, which keeps keys,
credentials and endpoints out of process arguments:

  S3CACHE_S3                bucket and key prefix, as s3://bucket/prefix (required)
  S3CACHE_KEY               cache key (required); a Go text template
  S3CACHE_ACCESS_KEY        S3 access key (required)
  S3CACHE_SECRET_KEY        S3 secret key (required)
  S3CACHE_SESSION_TOKEN     session token, for temporary credentials
  S3CACHE_REGION            AWS region, us-east-1 when unset
  S3CACHE_ENDPOINT_URL      S3-compatible endpoint to use instead of AWS
  S3CACHE_FORCE_PATH_STYLE  address the bucket path style; true when an endpoint is set
  S3CACHE_COMPRESSION       zstd (default), gzip or none
  S3CACHE_CROSS_PLATFORM    true to let archives match across operating systems
  S3CACHE_QUIET             true to log only errors
  S3CACHE_VERBOSE           true to log lookup and archive details

S3CACHE_KEY is a Go text template. Two functions are provided on top of the
text/template builtins:

  {{ env "NAME" }}            an environment variable, which must be set
  {{ hashFiles "glob" ... }}  sha256 over the matched file contents

Credentials are exactly what S3CACHE_ACCESS_KEY and S3CACHE_SECRET_KEY hold; the
AWS SDK chain (AWS_ACCESS_KEY_ID, profiles, instance roles) is not consulted.`

const saveDescription = `Archive every --path and upload the result under S3CACHE_KEY.

  S3CACHE_OVERWRITE         true to replace an existing entry instead of keeping it

Run "s3cache --help" for the shared environment options.`

const restoreDescription = `Restore the newest archive matching S3CACHE_KEY, falling back to the
--restore-key prefixes in the order given. A miss restores nothing and exits 0.

Run "s3cache --help" for the shared environment options.`

const (
	exitFailure = 1
	exitUsage   = 2
)

// Options carries the process boundaries so the CLI can be driven from tests.
type Options struct {
	Stdout io.Writer
	Stderr io.Writer
	// OpenStore overrides how the object store is built. Nil means S3.
	OpenStore func(store.S3Config) (store.Store, error)
}

// Run executes a command line and returns the process exit code.
func Run(ctx context.Context, args []string, opts Options) int {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	cmd := newRootCommand(opts)
	cmd.SetArgs(args)

	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return 0
	}

	code := exitUsage
	var exitErr *exitError
	if errors.As(err, &exitErr) {
		code = exitErr.code
	}
	fmt.Fprintf(opts.Stderr, "%v\n", err)
	return code
}

func newRootCommand(opts Options) *cobra.Command {
	root := &cobra.Command{
		Use:           "s3cache",
		Short:         "Store build caches in an S3 bucket",
		Long:          rootDescription,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			return usagef("a command is required: save or restore")
		},
	}
	root.SetOut(opts.Stdout)
	root.SetErr(opts.Stderr)
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newSaveCommand(opts), newRestoreCommand(opts))

	return root
}

// exitError carries the exit code an error should produce.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// usagef reports a mistake in how the command was invoked.
func usagef(format string, args ...any) error {
	return &exitError{code: exitUsage, err: fmt.Errorf(format, args...)}
}

// failuref reports a runtime failure.
func failuref(format string, args ...any) error {
	return &exitError{code: exitFailure, err: fmt.Errorf(format, args...)}
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
		return usagef("%s must not be empty", envVarName(optKey))
	}
	if strings.ContainsAny(key, "\x00\n\r") {
		return usagef("%s must not contain control characters", envVarName(optKey))
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
