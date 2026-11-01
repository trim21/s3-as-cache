# Cache storage format

What `s3cache` writes into a bucket, in enough detail for another tool (or a
future version of this one) to read it. Everything here is an implementation
contract: changing it is a breaking change, see [Compatibility](#compatibility).

## Object layout

```
s3://<bucket>/<prefix>/<platform>/<key><extension>
```

| Part | Source | Notes |
| --- | --- | --- |
| `<bucket>` | host of `S3CACHE_S3` | |
| `<prefix>` | path of `S3CACHE_S3`, `/`-trimmed | omitted when empty, so objects sit at the bucket root |
| `<platform>` | `GOOS-GOARCH` of the machine that saved | e.g. `linux-amd64`, `darwin-arm64`; the segment is omitted entirely when `S3CACHE_CROSS_PLATFORM` is set |
| `<key>` | `S3CACHE_KEY` after template expansion | may contain `/`, which nests the object; the full remainder of the name is the key |
| `<extension>` | `S3CACHE_COMPRESSION` | `.tar.zst` (zstd), `.tar.gz` (gzip) or `.tar` (none) |

Examples:

```
s3://build-cache/ci/linux-amd64/node-9f2c1e4d.tar.zst      zstd, platform specific
s3://build-cache/ci/darwin-arm64/rust/debug/abc123.tar.gz  gzip, nested key
s3://build-cache/ci/node-9f2c1e4d.tar.zst                  saved with S3CACHE_CROSS_PLATFORM
```

Why the platform segment: an archive stores absolute filesystem paths, so one
built on Windows or macOS must not be restored on Linux. Cross-platform caches
opt out by leaving the segment empty, which is why they can never match a
platform-specific object (the listing prefix differs).

There is no separate index, no version directory and no metadata object: the
object name is the only index, and everything else lives inside the archive.

## Lookup

`restore` performs exactly one listing of `<prefix>/<platform>/` and derives the
cache key of every object from its name:

1. build the listing prefix from `<prefix>` and `<platform>`;
2. for each listed object, drop the listing prefix and the compression
   extension — the remainder is that object's cache key (skip objects that do not
   end with the expected extension, which belong to another compression setting);
3. pick, in order:
   - the object whose cache key is exactly `S3CACHE_KEY`,
   - otherwise, for each `--restore-key` in the order given, the object whose
     cache key starts with that prefix;
4. when several objects match one step, the most recently modified wins
   (`LastModified`); ties are broken by the lexicographically greatest key, so the
   choice is deterministic;
5. a miss is not an error: nothing is restored and the command exits 0.

`save` does not list. It writes `<prefix>/<platform>/<key><extension>` directly,
conditionally (see [Writes](#writes)).

## Writes

Objects are immutable by default:

- `save` sends `If-None-Match: *` on the PUT, and first probes with `HEAD` because
  not every S3-compatible backend enforces the conditional header;
- an existing object therefore short-circuits the upload and the command exits 0
  with `cache entry "<key>" already exists, not overwriting`;
- `S3CACHE_OVERWRITE=true` drops both the probe and the header and replaces the
  object.

A cache key is therefore a claim on content: reusing a key with different
content is only possible with an explicit overwrite.

Objects up to 5 GiB are written with a single `PutObject`, larger archives go
through a multipart upload (64 MiB parts, 4 in flight). No object metadata,
tagging or content type is set; readers must not depend on any.

## Archive

The object body is a tar stream, compressed according to the extension. Readers
must decompress first and then read tar entries in order.

### Entry names

Entry names are the **absolute filesystem path of the file with the leading `/`
stripped**, using `/` separators:

```
home/runner/.cargo/registry/index/.../Cargo.toml
home/runner/work/app/target/debug/app
```

Restoring joins the cleaned name onto the filesystem root, so the example above
lands back in `/home/runner/...`. Names are cleaned with `path.Clean` against `/`
before use, which neutralises any `..` segment: extraction cannot write outside
the root.

Archived entry types:

| Type | Preserved |
| --- | --- |
| directory | mode; contents restore into it |
| regular file | mode, mtime, contents (owner/group from the tar header are ignored on restore) |
| symlink | link target verbatim (relative targets stay relative) |

Special files (fifos, sockets, devices) and hardlinks are skipped when saving and
ignored when restoring. Directory modes are applied after their contents are
extracted, so a read-only directory can still be filled.

### Manifest

The manifest is mandatory and must be the **first tar entry**, named exactly:

```
./.s3cache.json
```

A reader that sees anything else first rejects the archive (`ErrMissingManifest`)
instead of guessing. Keeping it first also means the metadata arrives before any
content, so reading stays streaming.

The `./` prefix is deliberate and load-bearing: non-manifest entries are absolute
paths without their leading `/`, so a cached file can never produce a name that
begins with `./`. Detection compares the raw tar entry name, not the basename, so
a real file called `.s3cache.json` (for example inside `node_modules`) is
archived and restored as ordinary data.

The body is UTF-8 JSON:

```json
{
  "paths": [
    "/home/runner/.cargo/registry",
    "!/home/runner/.cargo/registry/cache"
  ],
  "compression": "zstd",
  "platform": "linux-amd64",
  "created_at": "2026-11-01T09:41:12.238491Z"
}
```

| Field | Type | Meaning |
| --- | --- | --- |
| `paths` | string array | the `--path` patterns as given, normalised (`~` expanded, made absolute, `!` excludes kept with their prefix) |
| `compression` | string | `zstd`, `gzip` or `none`, written by the archiver so it always matches the stream |
| `platform` | string, optional | the platform segment this archive was saved under; absent or empty for cross-platform caches |
| `created_at` | string | RFC 3339 timestamp, UTC |

The manifest is metadata only: restores consume and skip it — it is never written
to the filesystem — and it is not counted in the "n files, n MiB" log line. It is
informational (it tells a reader what the archive claims to contain) and is
reported with `S3CACHE_VERBOSE=true`.

## Worked example

```bash
S3CACHE_S3=s3://build-cache/ci \
S3CACHE_KEY='linux-{{ hashFiles "Cargo.lock" }}' \
S3CACHE_ACCESS_KEY=... S3CACHE_SECRET_KEY=... \
  s3cache save --path ~/.cargo/registry --path target
```

on a Linux amd64 runner with the default compression produces:

```
s3://build-cache/ci/linux-amd64/linux-3f1a....tar.zst
```

whose first tar entry is

```json
{"paths":["/home/runner/.cargo/registry","/home/runner/work/app/target"],
 "compression":"zstd","platform":"linux-amd64","created_at":"2026-11-01T09:41:12Z"}
```

followed by `home/runner/.cargo/registry/...` and
`home/runner/work/app/target/...` entries. `s3cache restore` with the same
environment lists `ci/linux-amd64/`, matches the key, unpacks those two trees and
prints the key on stdout.

## Limits

- Object keys are capped by the backend (1024 bytes on S3); very long templated
  keys with long hash fragments can approach that.
- A single `PutObject` is limited to 5 GiB; larger archives use multipart.
- Nothing is pruned. Expire old objects with a bucket lifecycle rule on the
  prefix; because objects are immutable, an age-based rule is safe.

## Compatibility

Guaranteed to readers of this format, and safe to change without breaking them:

- adding new manifest fields (readers ignore unknown fields);
- adding new compression values — the extension tells a reader how to decompress,
  and unknown extensions are simply not matched;
- writing extra tar entries of the documented types, after the manifest.

Breaking, and therefore only allowed with a new layout (add a segment such as
`v2/` to the object path, or a new manifest name):

- changing the object layout or the meaning of a name segment;
- renaming or relocating the manifest entry, or changing entry-name semantics
  (absolute-path-minus-slash);
- changing matching rules (exact key, then prefix, newest wins).
