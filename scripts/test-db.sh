#!/usr/bin/env bash
# Runs the integration tests of database_middleware, user_service and calendar_service against a throwaway postgres:18 with all
# migrations applied. Needs Docker (migrate/migrate image) and Go.
# Usage: scripts/test-db.sh [service] [go test flags, e.g. -run X -v]   (default: both services)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MIGRATIONS="$ROOT/services/database_middleware/migrations"
NAME="mdd-test-db-$$"
PW="test-$RANDOM$RANDOM"
NET="$NAME-net"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker network create "$NET" >/dev/null
docker run -d --name "$NAME" --network "$NET" -p 127.0.0.1::5432 -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=praxis postgres:18 >/dev/null
for _ in $(seq 1 60); do
  docker exec "$NAME" pg_isready -U postgres -d praxis >/dev/null 2>&1 && break
  sleep 1
done
sleep 2

docker run --rm --network "$NET" -v "$MIGRATIONS:/migrations:ro" migrate/migrate -path=/migrations \
  -database "postgres://postgres:$PW@$NAME:5432/praxis?sslmode=disable" up >/dev/null
PORT="$(docker port "$NAME" 5432/tcp | head -n1 | sed 's/.*://')"

export TEST_DATABASE_URL="postgres://postgres:$PW@127.0.0.1:$PORT/praxis?sslmode=disable"
SERVICES="database_middleware user_service calendar_service"
# only services that exist in this repo yet (calendar_service arrives in Phase 6)
SERVICES="$(for s in $SERVICES; do if [ -d "$ROOT/services/$s" ]; then echo $s; fi; done)"
if [ $# -gt 0 ] && [ -d "$ROOT/services/$1" ]; then SERVICES="$1"; shift; fi
for svc in $SERVICES; do
  echo "== $svc"
  (cd "$ROOT/services/$svc" && go test -count=1 "$@" ./...)
done
