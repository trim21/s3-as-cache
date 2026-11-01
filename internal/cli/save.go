package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/trim21/s3-as-cache/internal/archive"
	"github.com/trim21/s3-as-cache/internal/cachekey"
	"github.com/trim21/s3-as-cache/internal/store"
)

func newSaveCommand(opts Options) *cobra.Command {
	var paths []string

	cmd := &cobra.Command{
		Use:     "save",
		Short:   "Archive the paths and upload them under the key",
		Long:    saveDescription,
		Example: "  s3cache save --path ~/.cargo/registry --path target",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSave(cmd.Context(), opts, paths)
		},
	}

	cmd.Flags().StringArrayVar(&paths, "path", nil, "file, directory or glob pattern to cache (repeatable; supports ** and !pattern)")

	return cmd
}

func runSave(ctx context.Context, opts Options, paths []string) error {
	conf, err := loadSaveSettings(newConfig())
	if err != nil {
		return err
	}

	log := newLogger(opts.Stderr, conf.quiet, conf.verbose)
	if len(paths) == 0 {
		return usagef("--path is required")
	}

	keys, err := expandKeys([]string{conf.key})
	if err != nil {
		return usagef("%s: %w", envVarName(optKey), err)
	}
	cacheKey := keys[0]
	if err := validateKey(cacheKey); err != nil {
		return err
	}
	log.debugf("saving under key %q", cacheKey)

	location, err := conf.resolve()
	if err != nil {
		return err
	}
	sess, err := openSession(opts, location, log)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "s3cache-*")
	if err != nil {
		return failuref("create temporary archive: %w", err)
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	manifest := archive.Manifest{
		Paths:     archive.NormalizePatterns(paths),
		Platform:  conf.platform(),
		CreatedAt: time.Now().UTC(),
	}
	stats, err := archive.Create(tmp, paths, sess.comp, manifest)
	if errors.Is(err, archive.ErrNoFiles) {
		log.infof("no files matched --path, skipping cache save")
		return nil
	}
	if err != nil {
		return failuref("create archive: %w", err)
	}

	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return failuref("inspect temporary archive: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return failuref("rewind temporary archive: %w", err)
	}

	objectKey := sess.prefix + cachekey.ArchiveKey(cacheKey, sess.comp.Extension())
	err = sess.store.Put(ctx, objectKey, tmp, size, !conf.overwrite)
	if errors.Is(err, store.ErrAlreadyExists) {
		log.infof("cache entry %q already exists, not overwriting", cacheKey)
		return nil
	}
	if err != nil {
		return failuref("upload archive for %s: %w", cacheKey, err)
	}

	log.infof("saved %d file(s), %s as %s, archive %s", stats.Files, humanBytes(stats.Bytes), cacheKey, humanBytes(size))
	fmt.Fprintln(opts.Stdout, cacheKey)
	return nil
}
