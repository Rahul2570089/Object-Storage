# Object-Storage

A lightweight, file-based object storage server in Go, being built into a **self-hosted cold-storage tier for observability data** (logs, traces, metric snapshots).

It comes with an HTTP API and an S3-style command-line client. The storage engine is hardened for the things an archive tier must get right: **atomic writes, streaming I/O, integrity checksums, and crash safety**.

> **Status:** the storage engine and a custom REST API are working and tested. S3 wire compatibility, compression, retention rules, and benchmarks are on the [roadmap](#roadmap). The API is *S3-like*, not yet S3-compatible.

## Why this exists

Observability platforms (log stores, metrics backends, trace stores) keep recent data on fast, expensive storage. After a few days that data is rarely queried, but it still has to be kept for debugging, audits, and retention policies. Keeping it all on the hot tier is the biggest driver of observability cost.

This project is the cheap tier behind it:

- **Who it's for:** teams that want a simple, self-hosted archive for telemetry and test artifacts without running a full distributed object store.
- **What it optimizes for:** correctness first (no partial or corrupt objects), then low operational overhead (a single static binary, plain files on disk).
- **How it reduces cost:** moves old data to commodity disk. Compression and automatic expiry are planned (see the roadmap).

## Features

| Area | Status |
|---|---|
| Bucket create / list | Done |
| Object upload, download, `HEAD`, delete, list | Done |
| `Range` requests (partial downloads) | Done (via `http.ServeContent`) |
| Streaming upload and download (constant memory) | Done |
| Atomic writes (temp file, fsync, rename) | Done |
| MD5 ETag and SHA-256 recorded per object | Done |
| Size check on every read, SHA-256 `Verify` | Done (verify is internal, not exposed over HTTP yet) |
| Per-object locking for concurrent writers | Done |
| Name validation (blocks path traversal) | Done |
| Crash recovery (temp files cleaned on startup) | Done |
| CLI client (`mb`, `ls`, `cp`, `rm`, `cat`, `stat`) | Done |
| S3-compatible API, auth, compression, retention, metrics | Planned |

## Architecture

```
 storage-cli / curl / (planned) aws cli, SDKs
            |  HTTP
            v
   [ HTTP handlers ]        cmd/server/handler.go
            |
            v
   [ Storage engine ]       cmd/server/service.go
     stream -> checksum -> temp file -> fsync -> rename -> metadata
            |
            v
   [ Local disk ]
     storage/data/       object bytes
     storage/metadata/   one JSON file per object and bucket
     storage/tmp/        in-flight uploads (wiped on startup)
```

### How a write works

1. The request body is streamed into a temp file in `storage/tmp/`. MD5 and SHA-256 are computed in the same pass, so memory use stays constant regardless of object size.
2. The temp file is `fsync`ed, so the bytes are on disk before the object becomes visible.
3. Under a per-object lock, the temp file is atomically `rename`d into `storage/data/<bucket>/<key>`. The parent directory is then fsynced (skipped on Windows, which has no directory fsync).
4. The metadata JSON is written last, also via temp file and rename. **An object exists only once its metadata does**, so a crash during upload never leaves a visible partial object.

If anything fails or the process is killed mid-upload, the temp file is discarded and the previous version of the object (if any) is untouched.

### Design decisions

- **Temp files live outside `data/`.** A crash never leaves junk next to real objects, and `ListObjects` never sees in-flight uploads.
- **Striped locks (256 `RWMutex`es keyed by a hash of bucket and key).** Only the short commit step is locked, so slow uploads don't block each other, and memory doesn't grow with the number of objects.
- **MD5 *and* SHA-256.** MD5 is the ETag that S3 clients expect; SHA-256 is used for integrity checks.
- **Metadata as sidecar JSON files.** Simple and easy to inspect. A real index would be needed for very large buckets.

## Requirements

- Go **1.24 or later** (see `go.mod`)
- `make` (optional; you can use `go build` directly)
- Linux, macOS, or WSL recommended. Windows works for development; see the notes under [Testing](#testing).

## Quick start

```bash
git clone https://github.com/Rahul2570089/Object-Storage.git
cd Object-Storage
make build
```

This creates `build/storage-server` and `build/storage-cli`.

> **Warning:** `make build` runs `make clean`, which deletes `./storage`. Don't run it in a directory whose data you want to keep. To rebuild without touching data, use `make server` or `make cli`.

**Start the server** (port 8080, data in `./storage`):

```bash
./build/storage-server
```

**Use the CLI in another terminal:**

```bash
./build/storage-cli mb my-bucket
echo "Hello World" > test.txt
./build/storage-cli cp test.txt my-bucket/test.txt
./build/storage-cli ls my-bucket
./build/storage-cli stat my-bucket/test.txt
./build/storage-cli cp my-bucket/test.txt downloaded.txt
```

Or run the scripted demo: `make demo`.

## HTTP API

| Method | Endpoint | Description | Success |
|---|---|---|---|
| `PUT` | `/buckets/{name}` | Create a bucket | `201` |
| `GET` | `/buckets` | List buckets | `200` |
| `PUT` | `/objects/{bucket}/{key}` | Upload an object (key may contain `/`) | `200` + metadata JSON |
| `GET` | `/objects/{bucket}/{key}` | Download an object (supports `Range`) | `200` / `206` |
| `HEAD` | `/objects/{bucket}/{key}` | Object headers only | `200` |
| `DELETE` | `/objects/{bucket}/{key}` | Delete an object | `204` |
| `GET` | `/objects/{bucket}` | List objects in a bucket | `200` |
| `GET` | `/health` | Health check | `200` |

Errors: `400` invalid bucket or key name, `404` bucket or object not found, `405` wrong method, `500` server error.

**Examples with curl:**

```bash
curl -X PUT http://localhost:8080/buckets/logs

curl -X PUT -H "Content-Type: application/gzip" \
     --data-binary @app-2026-11-21.log.gz \
     http://localhost:8080/objects/logs/app/2026-11-21.log.gz

curl -I http://localhost:8080/objects/logs/app/2026-11-21.log.gz

# first 1 KB only
curl -H "Range: bytes=0-1023" http://localhost:8080/objects/logs/app/2026-11-21.log.gz -o part.bin
```

## CLI reference

```
storage-cli [--server URL] [--verbose|-v] COMMAND [ARGS...]
```

Flags must come **before** the command.

| Command | Description | Example |
|---|---|---|
| `mb`, `makebucket` | Create a bucket | `storage-cli mb logs` |
| `ls`, `list` | List buckets, or objects in a bucket | `storage-cli ls logs` |
| `cp`, `copy` | Upload (`file bucket/key`) or download (`bucket/key file`) | `storage-cli cp a.log logs/a.log` |
| `rm`, `remove` | Delete an object | `storage-cli rm logs/a.log` |
| `cat` | Print an object to stdout | `storage-cli cat logs/a.log` |
| `stat` | Show object headers | `storage-cli stat logs/a.log` |
| `version`, `help` | Version and usage | |

Default server: `http://localhost:8080`.

## On-disk format

```
storage/
  data/<bucket>/<key>              object bytes
  metadata/<bucket>.json           bucket metadata
  metadata/<bucket>/<key>.json     object metadata
  tmp/                             in-flight uploads (cleared at startup)
```

Object metadata:

```json
{
  "key": "app/2026-11-21.log.gz",
  "size": 1048576,
  "content_type": "application/gzip",
  "etag": "md5-hex",
  "sha256": "sha256-hex",
  "last_modified": "2026-11-21T06:32:10Z"
}
```

## Testing

```bash
make test            # or: go test ./...
go test -race ./...  # recommended on Linux/macOS/WSL
make crash-test      # kill -9 the server mid-upload, restart, verify
```

Unit tests cover:

- Upload/download round trip with SHA-256 verification
- A failed upload leaves no object and no temp files
- A failed overwrite leaves the previous version intact
- Detection of silent data corruption (flipped bits, same size)
- 20 concurrent writers to one key never leave data and metadata disagreeing

The **crash test** (`scripts/crash_test.sh`) starts the server, streams a ~500 MB upload at a limited rate, `kill -9`s the server partway through, restarts it, and checks that the object returns `404` and no temp files remain.

Notes:

- Run `make crash-test` on Linux, macOS, or WSL. The script uses `kill -9`, `head`, and `/tmp`.
- Windows has no directory fsync, so on Windows the directory-sync step is skipped. Durability against power loss is only fully covered on Linux and macOS.

## Project layout

```
cmd/
  server/
    server.go        routing and startup
    handler.go       HTTP handlers
    service.go       storage engine (ObjectStorage)
    helper.go        validation, locks, atomic file helpers
    server_test.go   unit tests
  cli/
    client.go        command-line client
scripts/
  crash_test.sh      crash-injection test
makefile
```

## Limitations

Stated up front, so nothing here is a surprise:

- **Single node.** No replication or clustering. Next step: replication or erasure coding.
- **No authentication or authorization.** Run it on a trusted network only.
- **No encryption** at rest or in transit.
- **Not S3 wire-compatible yet.** The API is custom REST/JSON. S3 routes, XML responses, and SigV4 are planned, so `aws s3` and the AWS SDKs won't work yet.
- **No compression, retention rules, or metrics yet.**
- **No multipart upload and no versioning.** Last writer wins on concurrent writes to one key.
- **Listing is not paginated** and walks the bucket directory.
- **The ETag header is unquoted**, unlike S3.
- **File-per-key layout:** a key can't be both an object and a "folder" (e.g. `a` and `a/b`).

## Roadmap

- [x] **Week 1:** trustworthy engine: atomic writes, streaming, checksums, tests, crash test
- [ ] **Week 2:** S3-compatible API (path-style routes, XML, `ListObjectsV2`, auth), verified with `aws s3` and the AWS SDK
- [ ] **Week 3:** zstd compression, per-bucket lifecycle (auto-expiry), Prometheus metrics
- [ ] **Week 4:** real integration (Loki or Thanos backend, or an archiver CronJob)
- [ ] **Week 5:** Docker and Helm packaging, reproducible benchmarks, cost model
