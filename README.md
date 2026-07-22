# Document Store

An in-memory JSON document store written in Go. It exposes an HTTP API for creating stores of JSON documents, validating them against JSON Schema, applying patch operations to nested JSON, and authenticating with bearer tokens.

This project is a personal systems / backend learning exercise focused on request handling, concurrent data structures, schema validation, and modular package design.

## Features

- HTTP JSON document API: flat stores of documents addressed by id
- JSON Schema validation on write and patch
- Bearer-token authentication (crypto/rand tokens) with optional preloaded tokens
- RFC 6902 JSON Patch (add/remove/replace/move/copy/test)
- Optional Server-Sent Events watches on reads
- Ordered in-memory index: a hand-written copy-on-write B-tree, with a sharded variant for concurrent writers
- Debounced disk snapshot persistence so stores survive restarts
- Unauthenticated `/api/health` endpoint
- Package layout separating auth, storage, HTTP, schema, and patch logic

## Requirements

- Go 1.23+

No external database, Docker, or cloud services are required. Everything runs in-process.

## Quick start

The `Makefile` wraps the common flows:

```bash
make run    # build and run with schemas/schema1.json + token.json + data/snapshot.json
make demo   # end-to-end persistence demo (create, restart, verify)
make test   # go test ./...
make build  # build the docstore binary
```

Manually, without the Makefile:

```bash
git clone https://github.com/muradImre/database-design.git
cd database-design
go mod tidy
go build -o docstore .
./docstore --schema schemas/schema1.json --tokens token.json --data data/snapshot.json --port 8080
```

Or without building:

```bash
go run . --schema schemas/schema1.json --tokens token.json --data data/snapshot.json --port 8080
```

### CLI flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--port` | `8080` | TCP port to listen on |
| `--schema` | `schema.json` | JSON Schema file for document validation |
| `--tokens` | `tokens.json` | Username → token map for preloaded access tokens |
| `--data` | `data/snapshot.json` | Snapshot file for disk persistence (empty disables it) |
| `--index` | `cow` | Index implementation: `cow` (copy-on-write, lock-free reads) or `sharded` (concurrent writers) |
| `--verbose` | `false` | Debug logging |

### Persistence

Stores are held in memory but snapshotted to the `--data` file as JSON. Mutating
requests (PUT/POST/PATCH/DELETE) only mark the store dirty; a background writer
debounces bursts and performs the disk I/O off the request path, so writes never
block on the filesystem. A final snapshot is flushed on graceful shutdown
(Ctrl-C / SIGTERM). On startup the server loads the snapshot if the file exists,
so stores, documents, and metadata survive restarts. Pass `--data ""` to disable
persistence entirely.

### Health check

```bash
curl -s http://localhost:8080/api/health
# → {"status":"ok"}
```

`/api/health` requires no authentication.

`token.json` maps usernames to tokens, for example:

```json
{
  "benjamin": "benjamin_token",
  "victor": "victor_token",
  "murad": "murad_token"
}
```

## API overview

Authenticate either with a preloaded token or by creating a session:

```bash
# Create a session
curl -s -X POST http://localhost:8080/api/session \
  -H 'Content-Type: application/json' \
  -d '{"username":"demo"}'
# → {"access_token":"...","token_type":"Bearer","expires_in":3600}

# Or use a preloaded token from --tokens
export TOKEN=murad_token
```

Each store is a flat map of documents addressed by id. Any nesting lives inside
a document's JSON body (edited with patch), not as separate collection
resources. The path segments are:

```text
/api/stores/{store}            # a store
/api/stores/{store}/docs       # list documents / create with generated id
/api/stores/{store}/docs/{id}  # a single document
```

Examples:

```bash
# Create a store
curl -s -X PUT http://localhost:8080/api/stores/demo \
  -H "Authorization: Bearer $TOKEN"
# → {"href":"/api/stores/demo"}

# Put a document (must match the loaded schema)
curl -s -X PUT http://localhost:8080/api/stores/demo/docs/person1 \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ada","age":36}'

# Read it back
curl -s http://localhost:8080/api/stores/demo/docs/person1 \
  -H "Authorization: Bearer $TOKEN"

# List documents in a store (ordered by id, range-filterable)
curl -s 'http://localhost:8080/api/stores/demo/docs?range=a..z' \
  -H "Authorization: Bearer $TOKEN"

# Create a document with a server-generated id
curl -s -X POST http://localhost:8080/api/stores/demo/docs \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Bob","age":40}'

# Edit JSON in place with an RFC 6902 patch
curl -s -X PATCH http://localhost:8080/api/stores/demo/docs/person1 \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '[{"op":"add","path":"/nickname","value":"Countess"}]'
```

### Useful query parameters

| Param | Values | Meaning |
| ----- | ------ | ------- |
| `range` | `a..b`, `a..`, `..b`, `..` | Inclusive id range when listing a store |
| `if_exists` | `replace` (default), `fail` | Conflict behavior on document PUT (`fail` → HTTP 409) |
| `watch` | `1` / `true` | Open an SSE stream for a document or store listing |

Errors use a consistent envelope:

```json
{"error":{"code":"unauthorized","message":"missing or invalid token"}}
```

## Project layout

```text
.
├── auth/          # Session tokens (crypto/rand) + login/logout HTTP adapters
├── db/            # Flat document storage model + snapshot export/import
├── dbServer/      # HTTP API, routing, and debounced snapshot writer
├── patch/         # RFC 6902 JSON Patch implementation
├── persist/       # On-disk snapshot format and load/save helpers
├── schema/        # Schema parse + validate
├── btreeidx/      # Concurrent copy-on-write B-tree index
├── shardedidx/    # Sharded index variant for concurrent writers
├── pair/          # Generic key/value tuple for query results
├── schemas/       # Example JSON schemas
├── scripts/       # Demo scripts
├── main.go        # Process entrypoint
├── Makefile       # build / run / demo / test targets
└── go.mod
```

### Indexes

Each store's document map implements a shared `DBIndex` interface (`Find`, `Upsert`, `Remove`, `Query`). Two implementations are selectable via `--index`:

- **`cow` (default) — copy-on-write B-tree.** Nodes are immutable once published, so readers load the root pointer atomically and traverse a consistent snapshot with **no locks** — point lookups and range scans never block and never block writers. Writers are serialized by a single mutex and publish changes by cloning the root-to-leaf path and swapping in a new root atomically (single-writer / multi-reader MVCC).
- **`sharded` — striped concurrent index.** Keys are hashed across N independent COW B-trees, so writers to different keys proceed in parallel (one writer per shard). Queries fan out to every shard and merge; each shard is a consistent snapshot, though the merged cross-shard view is not a single atomic snapshot. This trades global snapshot atomicity for concurrent-writer throughput.

Benchmarks (`go test -bench . ./btreeidx/ ./shardedidx/`) cover lock-free read scaling and concurrent-writer throughput.

## Testing

```bash
go test ./...
```

## Notes

- Storage is in-memory with JSON snapshot persistence; stores survive restarts when `--data` is set.
- Preloaded tokens expire 24 hours after process start; session tokens expire after 1 hour.
- Keep real credentials out of the tokens file if you fork or publish changes.
