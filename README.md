# B-tree NoSQL DB

A lightweight NoSQL document database written in Go, built around a hand-written
**B-tree** storage engine. Documents are schemaless JSON grouped into stores and
addressed by id; ordering, range scans, and concurrency all come from the B-tree
index rather than a relational engine. It exposes an HTTP API for creating and
querying stores, validating documents against JSON Schema, applying RFC 6902
patches, streaming changes over Server-Sent Events, and authenticating with
bearer tokens issued via `POST /api/session`.

This is a personal systems / backend project focused on the parts that make a
NoSQL store interesting: an ordered key/value engine with disk snapshot
persistence, a concurrency model for that engine (copy-on-write reads plus a
sharded concurrent-writer variant), schema validation, and clean, decoupled
package design.

## Features

- HTTP JSON document API: flat stores of documents addressed by id
- JSON Schema validation on write and patch
- Bearer-token authentication via session login (`crypto/rand` tokens)
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
make run    # build and run with schemas/document.json + data/snapshot.json
make demo   # end-to-end persistence demo (create, restart, verify)
make test   # go test ./...
make build  # build the btreedb binary
```

Manually, without the Makefile:

```bash
git clone https://github.com/muradImre/database-design.git
cd database-design
go mod tidy
go build -o btreedb .
./btreedb --schema schemas/document.json --data data/snapshot.json --port 8080
```

Or without building:

```bash
go run . --schema schemas/document.json --data data/snapshot.json --port 8080
```

### CLI flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--port` | `8080` | TCP port to listen on |
| `--schema` | `schema.json` | JSON Schema file for document validation |
| `--data` | `data/snapshot.json` | Snapshot file for disk persistence (empty disables it) |
| `--index` | `cow` | Index implementation: `cow` (copy-on-write, lock-free reads) or `sharded` (concurrent writers) |
| `--tokens` | _(empty)_ | Optional username→token file for preloaded access tokens |
| `--verbose` | `false` | Debug logging |

### Persistence

Stores are held in memory but snapshotted to the `--data` file as JSON. Mutating
requests (PUT/POST/PATCH/DELETE) only mark the store dirty; a background writer
debounces bursts and performs the disk I/O off the request path, so writes never
block on the filesystem. A final snapshot is flushed on graceful shutdown
(Ctrl-C / SIGTERM). On startup the server loads the snapshot if the file exists,
so stores, documents, and metadata survive restarts. Pass `--data ""` to disable
persistence entirely.

Sessions are in-memory only: after a restart you create a new session with
`POST /api/session`. Document data is what persistence covers.

### Health check

```bash
curl -s http://localhost:8080/api/health
# → {"status":"ok"}
```

`/api/health` requires no authentication.

## API overview

Create a session, then use the returned bearer token:

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/session \
  -H 'Content-Type: application/json' \
  -d '{"username":"demo"}' | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
# → {"access_token":"...","token_type":"Bearer","expires_in":3600}
```

Each store is a flat map of documents addressed by id. Any nesting lives inside
a document's JSON body (edited with patch), not as separate collection
resources. The path segments are:

```text
/api/stores                     # list store names
/api/stores/{store}             # a store
/api/stores/{store}/docs        # list documents / create with generated id
/api/stores/{store}/docs/{id}   # a single document
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
├── schemas/       # Example JSON Schema (document validation)
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
- Session tokens expire after 1 hour and do not survive process restart.
- Preloaded tokens via `--tokens` are optional and mainly useful for automation.
