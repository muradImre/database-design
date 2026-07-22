#!/usr/bin/env bash
# Demonstrates disk snapshot persistence: create a store and document, restart
# the server, and confirm the document survives the restart.
set -euo pipefail

cd "$(dirname "$0")/.."

BINARY=./btreedb
SCHEMA=schemas/document.json
DATA=data/demo-snapshot.json
PORT="${DEMO_PORT:-18080}"
BASE="http://localhost:${PORT}"
LOG=/tmp/btreedb-demo.log

# Build if the binary is missing or out of date.
if [ ! -x "$BINARY" ]; then
  echo "==> Building $BINARY"
  go build -o "$BINARY" .
fi

# Start fresh so the demo is repeatable.
rm -f "$DATA"
mkdir -p data

SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

wait_for_health() {
  for _ in $(seq 1 50); do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
      echo "!! server exited early; log:" >&2
      cat "$LOG" >&2 || true
      return 1
    fi
    if curl -sf "${BASE}/api/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  echo "!! server did not become healthy; log:" >&2
  cat "$LOG" >&2 || true
  return 1
}

start_server() {
  "$BINARY" --schema "$SCHEMA" --data "$DATA" --port "$PORT" >"$LOG" 2>&1 &
  SERVER_PID=$!
  wait_for_health
}

stop_server() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
  fi
}

# Sessions live in memory, so each server process needs a fresh login.
login() {
  local session
  session=$(curl -s -X POST "${BASE}/api/session" \
    -H "Content-Type: application/json" \
    -d '{"username":"demo"}')
  TOKEN=$(printf '%s' "$session" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
  if [ -z "$TOKEN" ]; then
    echo "!! failed to create session: $session" >&2
    exit 1
  fi
  auth=(-H "Authorization: Bearer ${TOKEN}")
}

json=(-H "Content-Type: application/json")

echo "==> Starting server (first run)"
start_server

echo "==> Health check"
curl -s "${BASE}/api/health"; echo

echo "==> Creating session"
login
echo "got access token"

echo "==> Creating store 'demo'"
curl -s -X PUT "${BASE}/api/stores/demo" "${auth[@]}"; echo

echo "==> Putting document 'person1'"
curl -s -X PUT "${BASE}/api/stores/demo/docs/person1" "${auth[@]}" "${json[@]}" \
  -d '{"name":"Ada","age":36}'; echo

echo "==> Reading document back"
curl -s "${BASE}/api/stores/demo/docs/person1" "${auth[@]}"; echo

echo "==> Restarting server to prove persistence"
stop_server
start_server

echo "==> Creating a new session after restart"
login

echo "==> Reading document after restart"
RESULT=$(curl -s "${BASE}/api/stores/demo/docs/person1" "${auth[@]}")
echo "$RESULT"

if echo "$RESULT" | grep -q '"Ada"'; then
  echo "==> SUCCESS: document persisted across restart"
else
  echo "!! FAILURE: document did not persist" >&2
  exit 1
fi
