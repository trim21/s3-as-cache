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

Every option except the path list comes from the environment, which keeps keys,
templates and endpoints out of process arguments (and out of `ps`):

```bash
export S3CACHE_S3=s3://my-bucket/ci
export S3CACHE_KEY='linux-{{ hashFiles "Cargo.lock" }}'

# restore before the build
s3cache restore --restore-key linux- --path ~/.cargo/registry --path target

# build, then save
s3cache save --path ~/.cargo/registry --path target
```

Both commands print the resolved cache key on stdout and log progress on stderr,
so stdout stays usable in `$(...)`:

```bash
MATCHED=$(S3CACHE_S3=$S3 S3CACHE_KEY=$KEY s3cache restore --restore-key main- --path target)
```

`restore` exits 0 on a miss: a cache is an optimisation, so a cold run is not a
failure.

### Options

| Variable | Commands | Meaning |
| --- | --- | --- |
| `S3CACHE_S3` | both | `s3://bucket/prefix` holding the cache (required) |
| `S3CACHE_KEY` | both | exact cache key (required), a template — see [Cache keys](#cache-keys) |
| `S3CACHE_ACCESS_KEY` | both | S3 access key (required) |
| `S3CACHE_SECRET_KEY` | both | S3 secret key (required) |
| `S3CACHE_SESSION_TOKEN` | both | session token, for temporary credentials |
| `S3CACHE_REGION` | both | AWS region, `us-east-1` when unset |
| `S3CACHE_COMPRESSION` | both | `zstd` (default), `gzip` or `none` |
| `S3CACHE_ENDPOINT_URL` | both | S3-compatible endpoint instead of AWS |
| `S3CACHE_FORCE_PATH_STYLE` | both | address the bucket path style instead of virtual host style; defaults to `true` when `S3CACHE_ENDPOINT_URL` is set, set it to `false` to force virtual host style |
| `S3CACHE_CROSS_PLATFORM` | both | `true` to let archives match across OS and architecture |
| `S3CACHE_QUIET` | both | `true` to log errors only |
| `S3CACHE_VERBOSE` | both | `true` to log lookup and archive details |
| `S3CACHE_OVERWRITE` | save | `true` to replace an existing entry instead of keeping it |

Flags, all repeatable: `--path` (file, directory or glob, `**` and `!pattern`
supported) and, for `restore`, `--restore-key` (prefix fallback, tried in order).

Booleans accept anything `strconv.ParseBool` understands (`true`, `1`, `TRUE`).
A blank variable counts as unset, so an exported but empty `S3CACHE_KEY` is
reported as a missing key rather than used as one. `s3cache --help` prints the
same table.

Credentials are exactly `S3CACHE_ACCESS_KEY` / `S3CACHE_SECRET_KEY` (plus
`S3CACHE_SESSION_TOKEN` for temporary ones). The AWS SDK chain —
`AWS_ACCESS_KEY_ID`, profiles, container or instance roles — is not consulted, so
what the process signs with is always visible in one place. For third-party
storage also set `S3CACHE_ENDPOINT_URL` (and usually
`S3CACHE_FORCE_PATH_STYLE=true`).

### Cache keys

`S3CACHE_KEY` and `--restore-key` are [Go text templates](https://pkg.go.dev/text/template),
so a key can be derived from the environment and from lockfile contents instead
of being threaded through the CI YAML. A key without `{{ ... }}` actions is used
verbatim, and the usual template actions (`if`, `printf`, pipelines, ...) work.

| Function | Value |
| --- | --- |
| `env "NAME"` | the environment variable `NAME`; an unset variable is an error, so a missing value cannot silently collapse two caches into one key |
| `hashFiles "glob" ...` | SHA-256 over the contents of every file matched by the globs (relative to the working directory, `**` supported); the result does not depend on argument order or on where the workspace is checked out, and matching nothing is an error |

```bash
# Rust: key from Cargo.lock
S3CACHE_KEY='linux-{{ hashFiles "Cargo.lock" }}' \
  s3cache restore --restore-key linux- --path ~/.cargo/registry --path target

# Node: branch name plus both lockfiles
S3CACHE_KEY='{{ env "CI_COMMIT_REF_SLUG" }}-node-{{ hashFiles "package-lock.json" "pnpm-lock.yaml" }}' \
  s3cache save --path node_modules
```

Expansion happens before the key is used, and the expanded key is what `save`
prints on stdout, so the same template can be passed to a later `restore`.

## How it works

Archives are stored as:

```
s3://bucket/<prefix>/<version>/<key>.tar.zst
```

`<version>` is a hash of the normalized path list, the compression method and —
unless `S3CACHE_CROSS_PLATFORM` is set — `GOOS-GOARCH`. It keeps caches for different
path sets or platforms from matching each other, so adding or removing a `--path`
starts a fresh cache instead of restoring a partially wrong one.

A restore lists the `<version>/` prefix once and picks, in order:

1. the object whose key is exactly `S3CACHE_KEY`,
2. for each `--restore-key` in order, the most recently written object whose key
   starts with that prefix.

`save` writes with a conditional create, so an existing key is kept and the
upload is skipped (`S3CACHE_OVERWRITE=true` forces a replacement). Nothing matched by
`--path` means nothing is uploaded, which mirrors "cache nothing" rather than
storing an empty archive.

Archives are tar streams with entries rooted at the filesystem root, so a restore
of `/home/runner/.cargo` writes to `/home/runner/.cargo` and needs write access
there. Extraction cleans every entry against the root and cannot escape it.

## CI examples

GitHub Actions:

```yaml
- name: Restore cache
  run: s3cache restore --restore-key linux- --path ~/.cargo/registry --path target
  env:
    S3CACHE_S3: s3://my-bucket/ci
    S3CACHE_KEY: linux-${{ hashFiles('Cargo.lock') }}
    S3CACHE_ACCESS_KEY: ${{ secrets.S3CACHE_ACCESS_KEY }}
    S3CACHE_SECRET_KEY: ${{ secrets.S3CACHE_SECRET_KEY }}
    S3CACHE_REGION: eu-west-1

- run: cargo build --release

- name: Save cache
  run: s3cache save --path ~/.cargo/registry --path target
  env:
    S3CACHE_S3: s3://my-bucket/ci
    S3CACHE_KEY: linux-${{ hashFiles('Cargo.lock') }}
    S3CACHE_ACCESS_KEY: ${{ secrets.S3CACHE_ACCESS_KEY }}
    S3CACHE_SECRET_KEY: ${{ secrets.S3CACHE_SECRET_KEY }}
    S3CACHE_REGION: eu-west-1
```

GitLab CI:

```yaml
cache-deps:
  variables:
    S3CACHE_S3: s3://my-bucket/ci
    S3CACHE_KEY: 'linux-{{ env "CI_COMMIT_REF_SLUG" }}-{{ hashFiles "Cargo.lock" }}'
  script:
    - s3cache restore --restore-key "linux-$CI_COMMIT_REF_SLUG-" --path target
    - cargo build --release
    - s3cache save --path target
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
S3CACHE_TEST_ACCESS_KEY=s3cache S3CACHE_TEST_SECRET_KEY=s3cache-secret \
go test ./... -run Integration -v
```

## Limitations

- Linux and macOS only; Windows path semantics (drive letters) are not handled.
- No cache pruning: use an S3 lifecycle rule on the prefix to expire old objects.
- Restored paths are absolute, so a restore must be able to write them.
