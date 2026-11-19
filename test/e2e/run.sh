#!/usr/bin/env bash
# End-to-end smoke test for the Magento tools.
#
# Requires: docker compose (PostgreSQL), go, curl.
# It starts a fake Magento API, the gateway server and worker on loopback,
# exercises product creation through the confirmation flow, the SEO audit,
# import validation, and an asynchronous import, then cleans everything up.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
PG_PORT="${E2E_PG_PORT:-5433}"
API_PORT="${E2E_API_PORT:-8081}"
MAGENTO_PORT="${E2E_MAGENTO_PORT:-8090}"
PASSWORD="${E2E_PG_PASSWORD:-mage2mcp-dev}"

cleanup() {
  set +e
  [[ -n "${WORKER_PID:-}" ]] && kill "$WORKER_PID" 2>/dev/null
  [[ -n "${SERVER_PID:-}" ]] && kill "$SERVER_PID" 2>/dev/null
  [[ -n "${MAGENTO_PID:-}" ]] && kill "$MAGENTO_PID" 2>/dev/null
  docker compose -f "$ROOT/docker-compose.yml" -f "$WORK/compose.override.yml" down -v >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "== build =="
mkdir -p "$WORK/bin"
(cd "$ROOT" && go build -o "$WORK/bin/server" ./cmd/server)
(cd "$ROOT" && go build -o "$WORK/bin/worker" ./cmd/worker)
(cd "$ROOT" && go build -o "$WORK/bin/admin" ./cmd/admin)
(cd "$ROOT" && go build -o "$WORK/bin/fake-magento" ./test/e2e/fakemagento)
(cd "$ROOT" && go build -o "$WORK/bin/client" ./test/e2e/client)

cat > "$WORK/compose.override.yml" <<YAML
services:
  postgres:
    ports:
      - "127.0.0.1:${PG_PORT}:5432"
YAML
echo "== postgres =="
docker compose -f "$ROOT/docker-compose.yml" -f "$WORK/compose.override.yml" up -d postgres
pg_ready=0
for _ in $(seq 1 60); do
  if docker compose -f "$ROOT/docker-compose.yml" -f "$WORK/compose.override.yml" exec -T postgres pg_isready -U mage2mcp >/dev/null 2>&1; then
    pg_ready=1
    break
  fi
  sleep 1
done
if [[ "$pg_ready" != "1" ]]; then
  echo "E2E FAIL: postgres did not become ready"
  docker compose -f "$ROOT/docker-compose.yml" -f "$WORK/compose.override.yml" logs postgres | tail -30
  exit 1
fi

cat > "$WORK/config.yaml" <<YAML
server:
  listen: "127.0.0.1:${API_PORT}"
  shutdown_grace: 5s
  max_body_bytes: 1048576
auth:
  enabled: false
log:
  level: info
  format: json
tools:
  writes: true
database:
  url: "postgres://mage2mcp:${PASSWORD}@127.0.0.1:${PG_PORT}/mage2mcp?sslmode=disable"
  max_conns: 5
datasources:
  magento:
    enabled: true
    instances:
      - id: store-us
        endpoint: "http://127.0.0.1:${MAGENTO_PORT}"
        store_code: default
        credential: "bearer:e2e-token"
YAML

echo "== fake magento =="
"$WORK/bin/fake-magento" "$MAGENTO_PORT" >"$WORK/magento.log" 2>&1 &
MAGENTO_PID=$!

echo "== server =="
"$WORK/bin/server" -config "$WORK/config.yaml" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
server_ready=0
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${API_PORT}/healthz" >/dev/null 2>&1; then
    server_ready=1
    break
  fi
  sleep 1
done
if [[ "$server_ready" != "1" ]]; then
  echo "E2E FAIL: server did not become healthy"
  tail -30 "$WORK/server.log"
  exit 1
fi

echo "== worker =="
"$WORK/bin/worker" -config "$WORK/config.yaml" >"$WORK/worker.log" 2>&1 &
WORKER_PID=$!

export E2E_ENDPOINT="http://127.0.0.1:${API_PORT}/mcp"
export E2E_INSTANCE="store-us"
client() { "$WORK/bin/client" "$@"; }
approve() { "$WORK/bin/admin" -config "$WORK/config.yaml" -as e2e-reviewer approve "$1" >/dev/null; }

fail() { echo "E2E FAIL: $*"; echo "--- server ---"; tail -20 "$WORK/server.log"; echo "--- worker ---"; tail -20 "$WORK/worker.log"; exit 1; }

echo "== tools/list =="
client tools | grep -q "TOOLS OK" || fail "tools/list"

echo "== product create through confirmation =="
out=$(client create)
cid=$(echo "$out" | awk '/CONFIRMATION/{print $2}')
[[ -n "$cid" ]] || fail "no confirmation id: $out"
approve "$cid"
out=$(client create-retry "$cid" 2>&1)
echo "$out"
echo "$out" | grep -q "CREATED" || fail "create retry"

echo "== SEO audit =="
client seo | grep -q "SEO SCORE" || fail "seo audit"

echo "== import validation warnings =="
client validate | grep -q "warnings=" || fail "import validation"

echo "== asynchronous import =="
out=$(client import-start)
cid=$(echo "$out" | awk '/CONFIRMATION/{print $2}')
[[ -n "$cid" ]] || fail "no import confirmation id: $out"
approve "$cid"
job=$(client import-retry "$cid" | awk '/JOB/{print $2}')
[[ -n "$job" ]] || fail "no job id"
client import-status "$job" | tee "$WORK/import-status.txt" | grep -q "JOB succeeded" || fail "import did not succeed"
grep -q '"created":1' "$WORK/import-status.txt" || fail "import report missing created=1"

echo "== configurable product =="
out=$(client configurable)
cid=$(echo "$out" | awk '/CONFIRMATION/{print $2}')
[[ -n "$cid" ]] || fail "no configurable confirmation id: $out"
approve "$cid"
out=$(client configurable-retry "$cid" 2>&1)
echo "$out"
echo "$out" | grep -q "CREATED CONFIGURABLE" || fail "configurable retry"

echo "== order creation =="
out=$(client order)
cid=$(echo "$out" | awk '/CONFIRMATION/{print $2}')
[[ -n "$cid" ]] || fail "no order confirmation id: $out"
approve "$cid"
out=$(client order-retry "$cid" 2>&1)
echo "$out"
echo "$out" | grep -q "ORDER 99" || fail "order retry"

echo "E2E PASS"
