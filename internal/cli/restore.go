package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/trim21/s3-as-cache/internal/archive"
	"github.com/trim21/s3-as-cache/internal/cachekey"
	"github.com/trim21/s3-as-cache/internal/store"
)

func runRestore(ctx context.Context, args []string, opts Options) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(opts.Stderr)
	fs.Usage = func() {
		fmt.Fprint(opts.Stderr, "usage: s3cache restore --s3 s3://bucket/prefix --key KEY [--restore-key PREFIX]... --path PATH...\n\nflags:\n")
		fs.PrintDefaults()
	}

	var (
		common      commonFlags
		paths       multiFlag
		restoreKeys multiFlag
	)
	common.register(fs)
	fs.Var(&paths, "path", "file, directory or glob pattern to restore (repeatable; supports ** and !pattern)")
	fs.Var(&restoreKeys, "restore-key", "fallback key matched by prefix, tried in the given order (repeatable)")
	key := fs.String("key", "", "cache key to restore; Go text template, e.g. {{ hashFiles \"Cargo.lock\" }}")
	lookupOnly := fs.Bool("lookup-only", false, "report the matching key without downloading the archive")
	failOnMiss := fs.Bool("fail-on-miss", false, "exit non-zero when no cached entry matches")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	log := newLogger(opts.Stderr, common.quiet, common.verbose)
	if len(paths) == 0 {
		log.errorf("--path is required")
		return 2
	}

	keys, err := expandKeys(append([]string{*key}, restoreKeys...))
	if err != nil {
		log.errorf("%v", err)
		return 2
	}
	primaryKey, fallbackKeys := keys[0], keys[1:]
	if err := validateKey(primaryKey); err != nil {
		log.errorf("%v", err)
		return 2
	}
	log.debugf("looking up key %q with restore keys %q", primaryKey, fallbackKeys)

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

	objects, err := sess.store.List(ctx, sess.prefix)
	if err != nil {
		log.errorf("list cache entries under %s: %v", sess.prefix, err)
		return 1
	}

	extension := sess.comp.Extension()
	candidates := make([]store.Object, 0, len(objects))
	for _, obj := range objects {
		matched, ok := cachekey.KeyFromObject(obj.Key, sess.prefix, extension)
		if !ok {
			continue
		}
		candidates = append(candidates, store.Object{
			Key:          matched,
			Size:         obj.Size,
			LastModified: obj.LastModified,
		})
	}
	log.debugf("listed %d object(s) under %s, %d usable", len(objects), sess.prefix, len(candidates))

	lookedUp := strings.Join(append([]string{primaryKey}, fallbackKeys...), ", ")
	match, ok := cachekey.Match(candidates, primaryKey, fallbackKeys)
	if !ok {
		if *failOnMiss {
			log.errorf("cache miss for keys: %s", lookedUp)
			return 1
		}
		log.infof("cache miss for keys: %s", lookedUp)
		return 0
	}

	if match.Key != primaryKey {
		log.infof("no entry for %s, falling back to %s", primaryKey, match.Key)
	}

	if *lookupOnly {
		log.infof("cache hit: %s (%s)", match.Key, humanBytes(match.Size))
		fmt.Fprintln(opts.Stdout, match.Key)
		return 0
	}

	body, err := sess.store.Get(ctx, sess.prefix+cachekey.ArchiveKey(match.Key, extension))
	if err != nil {
		log.errorf("download %s: %v", match.Key, err)
		return 1
	}
	stats, extractErr := archive.Extract(body, sess.comp, "/")
	if closeErr := body.Close(); extractErr == nil && closeErr != nil {
		extractErr = closeErr
	}
	if extractErr != nil {
		log.errorf("extract %s: %v", match.Key, extractErr)
		return 1
	}

	log.infof("restored %d file(s), %s from %s", stats.Files, humanBytes(stats.Bytes), match.Key)
	fmt.Fprintln(opts.Stdout, match.Key)
	return 0
}
