#!/usr/bin/env bash
# Applies the migrations up, down, up against a throwaway postgres:16 and runs scripts/test-schema.sql.
# Needs only Docker (uses the migrate/migrate image, no local install).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MIGRATIONS="$ROOT/services/database_middleware/migrations"
NAME="sbd-migrate-test-$$"
PW="test-$RANDOM$RANDOM"
NET="$NAME-net"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker network create "$NET" >/dev/null
docker run -d --name "$NAME" --network "$NET" -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=praxis postgres:16 >/dev/null

for _ in $(seq 1 60); do
  docker exec "$NAME" pg_isready -U postgres -d praxis >/dev/null 2>&1 && break
  sleep 1
done
sleep 2

URL="postgres://postgres:$PW@$NAME:5432/praxis?sslmode=disable"
migrate() { docker run --rm --network "$NET" -v "$MIGRATIONS:/migrations:ro" migrate/migrate -path=/migrations -database "$URL" "$@"; }

echo "== up";   migrate up
echo "== down"; migrate down -all
echo "== up";   migrate up
echo "== schema tests"
docker exec -i "$NAME" psql -U postgres -d praxis -v ON_ERROR_STOP=1 < "$ROOT/scripts/test-schema.sql"
echo "OK: migrations up/down/up and schema tests passed"
