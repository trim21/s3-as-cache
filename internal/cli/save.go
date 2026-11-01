package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/trim21/s3-as-cache/internal/archive"
	"github.com/trim21/s3-as-cache/internal/cachekey"
	"github.com/trim21/s3-as-cache/internal/store"
)

func runSave(ctx context.Context, args []string, opts Options) int {
	fs := flag.NewFlagSet("save", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	fs.Usage = func() {
		fmt.Fprint(opts.Stderr, "usage: s3cache save --s3 s3://bucket/prefix --key KEY --path PATH...\n\nflags:\n")
		fs.PrintDefaults()
	}

	var (
		common commonFlags
		paths  multiFlag
	)
	common.register(fs)
	fs.Var(&paths, "path", "file, directory or glob pattern to cache (repeatable; supports ** and !pattern)")
	key := fs.String("key", "", "cache key to save under; Go text template, e.g. {{ hashFiles \"Cargo.lock\" }}")
	overwrite := fs.Bool("overwrite", false, "replace an entry that already exists instead of keeping it")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	log := newLogger(opts.Stderr, common.quiet, common.verbose)

	keys, err := expandKeys([]string{*key})
	if err != nil {
		log.errorf("%v", err)
		return 2
	}
	cacheKey := keys[0]
	if err := validateKey(cacheKey); err != nil {
		log.errorf("%v", err)
		return 2
	}
	log.debugf("saving under key %q", cacheKey)
	if len(paths) == 0 {
		log.errorf("--path is required")
		return 2
	}

	location, err := common.resolve(paths)
	if err != nil {
		log.errorf("%v", err)
		return 2
	}
	sess, err := openSession(ctx, opts, location, log)
	if err != nil {
		log.errorf("%v", err)
		return 1
	}

	tmp, err := os.CreateTemp("", "s3cache-*")
	if err != nil {
		log.errorf("create temporary archive: %v", err)
		return 1
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	stats, err := archive.Create(tmp, paths, sess.comp)
	if errors.Is(err, archive.ErrNoFiles) {
		log.infof("no files matched --path, skipping cache save")
		return 0
	}
	if err != nil {
		log.errorf("create archive: %v", err)
		return 1
	}

	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		log.errorf("inspect temporary archive: %v", err)
		return 1
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		log.errorf("rewind temporary archive: %v", err)
		return 1
	}

	objectKey := sess.prefix + cachekey.ArchiveKey(cacheKey, sess.comp.Extension())
	err = sess.store.Put(ctx, objectKey, tmp, size, !*overwrite)
	if errors.Is(err, store.ErrAlreadyExists) {
		log.infof("cache entry %q already exists, not overwriting", cacheKey)
		return 0
	}
	if err != nil {
		log.errorf("upload archive for %s: %v", cacheKey, err)
		return 1
	}

	log.infof("saved %d file(s), %s as %s, archive %s", stats.Files, humanBytes(stats.Bytes), cacheKey, humanBytes(size))
	fmt.Fprintln(opts.Stdout, cacheKey)
	return 0
}
