#!/usr/bin/env bash
# Fills the database with demo content (specialties, services, staff, doctors, patients, bookings). Safe to run twice.
# Demo data only: all accounts share DEMO_PASSWORD and have to enrol MFA at the first login.
# Usage: scripts/seed.sh              seed the running cluster (make up first)
#        scripts/seed.sh --sql        only print the SQL (pipe it into any psql)
# Env:   DEMO_PASSWORD (default in cmd/seed, min. 15 characters, must pass the password policy)
set -euo pipefail
source "$(dirname "$0")/lib.sh"

need go "Install Go."
gen() { (cd "$ROOT/services/user_service" && go run ./cmd/seed -seeds "$ROOT/services/database_middleware/seeds"); }

if [ "${1:-}" = "--sql" ]; then gen; exit 0; fi

check_tools
cluster_exists || die "The cluster '$CLUSTER' does not exist. Run: make up"

sql="$(gen)"
log "Seeding demo data"
kn exec -i postgres-0 -- psql -U postgres -d praxis -v ON_ERROR_STOP=1 -q <<<"$sql"
log "Done. Log in at $BASE_URL with e.g. organisator@demo.example.test (see the password above)"
