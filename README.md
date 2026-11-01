# s3cache

Build cache that lives directly in an S3 bucket. One static Go binary, no cache
server, no proxy, no CI-specific integration: if your CI can run a binary and
reach S3, it can restore and save caches.

Works with AWS S3 and any S3-compatible endpoint (Silo, MinIO, Cloudflare R2,
Backblaze B2, Hetzner, ...).

## Install

```bash
go install github.com/trim21/s3-as-cache/cmd/s3cache@latest
```

Or build a static binary for a target platform:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o s3cache ./cmd/s3cache
```

## Usage

```bash
# restore before the build
s3cache restore --s3 s3://my-bucket/ci \
  --key "linux-$(sha256sum Cargo.lock | cut -d' ' -f1)" \
  --restore-key linux- \
  --path ~/.cargo/registry --path target

# save after a miss
s3cache save --s3 s3://my-bucket/ci \
  --key "linux-$(sha256sum Cargo.lock | cut -d' ' -f1)" \
  --path ~/.cargo/registry --path target
```

Both commands print the resolved cache key on stdout and log progress on stderr,
so stdout stays usable in `$(...)`:

```bash
MATCHED=$(s3cache restore --s3 "$S3" --key "$KEY" --restore-key main- --path target)
```

`restore` exits 0 on a miss (a cache is an optimisation), pass `--fail-on-miss`
when the build depends on it. `--lookup-only` reports the matching key without
downloading anything.

### Flags

| Flag | Commands | Meaning |
| --- | --- | --- |
| `--s3` | both | `s3://bucket/prefix` holding the cache (required) |
| `--key` | both | exact cache key (required), templates allowed — see [Cache keys](#cache-keys) |
| `--path` | both | file, directory or glob to cache; repeatable, supports `**` and `!pattern` excludes |
| `--restore-key` | restore | fallback key matched by prefix, tried in order; repeatable |
| `--lookup-only` | restore | only report the matching key |
| `--fail-on-miss` | restore | exit 1 when nothing matches |
| `--overwrite` | save | replace an existing entry instead of keeping it |
| `--compression` | both | `zstd` (default), `gzip` or `none` |
| `--cross-platform` | both | let archives match across OS and architecture |
| `--region` | both | AWS region; defaults to the AWS SDK configuration |
| `--endpoint-url` | both | S3-compatible endpoint instead of AWS |
| `--force-path-style` | both | address the bucket path style instead of virtual host style |
| `--quiet` | both | log errors only |
| `--verbose` | both | log lookup and archive details |

Credential resolution is the AWS SDK default chain: `AWS_ACCESS_KEY_ID` /
`AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN`, `AWS_PROFILE` and the shared
config file, container credentials, or an instance profile. For third-party
storage also set `--endpoint-url` (and usually `--force-path-style`).

### Cache keys

`--key` and `--restore-key` are [Go text templates](https://pkg.go.dev/text/template),
so a key can be derived from the environment and from lockfile contents instead
of being threaded through the CI YAML. A key without `{{ ... }}` actions is used
verbatim, and the usual template actions (`if`, `printf`, pipelines, ...) work.

| Function | Value |
| --- | --- |
| `env "NAME"` | the environment variable `NAME`; an unset variable is an error, so a missing value cannot silently collapse two caches into one key |
| `hashFiles "glob" ...` | SHA-256 over the contents of every file matched by the globs (relative to the working directory, `**` supported); the result does not depend on argument order or on where the workspace is checked out, and matching nothing is an error |

```bash
# Rust: key from Cargo.lock
s3cache restore --s3 s3://b/ci --key 'linux-{{ hashFiles "Cargo.lock" }}' \
  --restore-key linux- --path ~/.cargo/registry --path target

# Node: branch name plus both lockfiles
s3cache save --s3 s3://b/ci \
  --key '{{ env "CI_COMMIT_REF_SLUG" }}-node-{{ hashFiles "package-lock.json" "pnpm-lock.yaml" }}' \
  --path node_modules
```

Expansion happens before the key is used, and the expanded key is what `save`
prints on stdout, so the same template can be passed to a later `restore`. Quote
templates in your shell so `{{ }}` reaches the binary untouched.

## How it works

Archives are stored as:

```
s3://bucket/<prefix>/<version>/<key>.tar.zst
```

`<version>` is a hash of the normalized path list, the compression method and —
unless `--cross-platform` is given — `GOOS-GOARCH`. It keeps caches for different
path sets or platforms from matching each other, so adding or removing a `--path`
starts a fresh cache instead of restoring a partially wrong one.

A restore lists the `<version>/` prefix once and picks, in order:

1. the object whose key is exactly `--key`,
2. for each `--restore-key` in order, the most recently written object whose key
   starts with that prefix.

`save` writes with a conditional create, so an existing key is kept and the
upload is skipped (`--overwrite` forces a replacement). Nothing matched by
`--path` means nothing is uploaded, which mirrors "cache nothing" rather than
storing an empty archive.

Archives are tar streams with entries rooted at the filesystem root, so a restore
of `/home/runner/.cargo` writes to `/home/runner/.cargo` and needs write access
there. Extraction cleans every entry against the root and cannot escape it.

## CI examples

GitHub Actions:

```yaml
- uses: actions/checkout@v5
- run: curl -fsSL https://example.com/s3cache-linux-amd64 -o /usr/local/bin/s3cache && chmod +x /usr/local/bin/s3cache
  env: ...
- name: Restore cache
  run: s3cache restore --s3 "$S3_URL" --key "linux-${{ hashFiles('Cargo.lock') }}" --restore-key linux- --path target
  env:
    S3_URL: s3://my-bucket/ci
    AWS_ACCESS_KEY_ID: ${{ secrets.AWS_ACCESS_KEY_ID }}
    AWS_SECRET_ACCESS_KEY: ${{ secrets.AWS_SECRET_ACCESS_KEY }}
    AWS_REGION: us-east-1
- run: cargo build --release
- name: Save cache
  run: s3cache save --s3 "$S3_URL" --key "linux-${{ hashFiles('Cargo.lock') }}" --path target
  env: ...
```

GitLab CI:

```yaml
cache-deps:
  variables:
    S3_URL: s3://my-bucket/ci
  script:
    - s3cache restore --s3 "$S3_URL" --key "linux-$CI_COMMIT_REF_SLUG-$(sha256sum Cargo.lock | cut -d' ' -f1)" --restore-key "linux-$CI_COMMIT_REF_SLUG-" --path target
    - cargo build --release
    - s3cache save --s3 "$S3_URL" --key "linux-$CI_COMMIT_REF_SLUG-$(sha256sum Cargo.lock | cut -d' ' -f1)" --path target
```

## Development

```bash
go test ./...            # unit tests, S3 tests skip themselves
go vet ./...
gofmt -l .
```

The integration tests run against a real S3 backend and are skipped unless it is
configured. CI starts [Silo](https://github.com/pgsty/silo) for them; locally:

```bash
docker run -d --name silo -p 9000:9000 \
  -e MINIO_ROOT_USER=s3cache -e MINIO_ROOT_PASSWORD=s3cache-secret \
  docker.io/pgsty/silo:RELEASE.2026-10-26T00-00-00Z server /data
docker exec silo mcli alias set local http://127.0.0.1:9000 s3cache s3cache-secret
docker exec silo mcli mb local/cache-integration

S3CACHE_TEST_S3=s3://cache-integration/ci \
S3CACHE_TEST_ENDPOINT=http://127.0.0.1:9000 \
AWS_ACCESS_KEY_ID=s3cache AWS_SECRET_ACCESS_KEY=s3cache-secret AWS_REGION=us-east-1 \
go test ./... -run Integration -v
```

## Limitations

- Linux and macOS only; Windows path semantics (drive letters) are not handled.
- No cache pruning: use an S3 lifecycle rule on the prefix to expire old objects.
- Restored paths are absolute, so a restore must be able to write them.
