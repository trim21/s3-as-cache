package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trim21/s3-as-cache/internal/archive"
	"github.com/trim21/s3-as-cache/internal/cachekey"
	"github.com/trim21/s3-as-cache/internal/store"
)

func newRestoreCommand(opts Options) *cobra.Command {
	var restoreKeys []string

	cmd := &cobra.Command{
		Use:     "restore",
		Short:   "Restore the newest archive matching the key",
		Long:    restoreDescription,
		Example: "  s3cache restore --restore-key linux-",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRestore(cmd.Context(), opts, restoreKeys)
		},
	}

	cmd.Flags().StringArrayVar(&restoreKeys, "restore-key", nil, "fallback key matched by prefix, tried in the given order (repeatable)")

	return cmd
}

func runRestore(ctx context.Context, opts Options, restoreKeys []string) error {
	conf, err := loadSettings(newConfig())
	if err != nil {
		return err
	}

	log := newLogger(opts.Stderr, conf.quiet, conf.verbose)

	keys, err := expandKeys(append([]string{conf.key}, restoreKeys...))
	if err != nil {
		return usagef("%s: %w", envVarName(optKey), err)
	}
	primaryKey, fallbackKeys := keys[0], keys[1:]
	if err := validateKey(primaryKey); err != nil {
		return err
	}
	log.debugf("looking up key %q with restore keys %q", primaryKey, fallbackKeys)

	location, err := conf.resolve()
	if err != nil {
		return err
	}
	sess, err := openSession(opts, location, log)
	if err != nil {
		return err
	}

	objects, err := sess.store.List(ctx, sess.prefix)
	if err != nil {
		return failuref("list cache entries under %s: %w", sess.prefix, err)
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
		log.infof("cache miss for keys: %s", lookedUp)
		return nil
	}

	if match.Key != primaryKey {
		log.infof("no entry for %s, falling back to %s", primaryKey, match.Key)
	}

	body, err := sess.store.Get(ctx, sess.prefix+cachekey.ArchiveKey(match.Key, extension))
	if err != nil {
		return failuref("download %s: %w", match.Key, err)
	}
	stats, manifest, extractErr := archive.Extract(body, sess.comp, "/")
	if closeErr := body.Close(); extractErr == nil && closeErr != nil {
		extractErr = closeErr
	}
	if extractErr != nil {
		return failuref("extract %s: %w", match.Key, extractErr)
	}

	log.debugf("archive paths: %s", strings.Join(manifest.Paths, ", "))

	log.infof("restored %d file(s), %s from %s", stats.Files, humanBytes(stats.Bytes), match.Key)
	fmt.Fprintln(opts.Stdout, match.Key)
	return nil
}
