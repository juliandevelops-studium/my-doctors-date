# Shared settings and helpers for the cluster scripts.
# shellcheck shell=bash

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="$ROOT/.tools/bin:$PATH"

CLUSTER="${PRAXIS_CLUSTER:-praxis}"
NS="praxis"
K3S_IMAGE="rancher/k3s:v1.37.1-k3s1"
HOST="${PRAXIS_HOST:-praxis.localhost}"
HTTPS_PORT="${PRAXIS_HTTPS_PORT:-8443}"
BASE_URL="https://$HOST:$HTTPS_PORT"
SECRETS_DIR="$ROOT/secrets"
IMAGE_TAG="${PRAXIS_IMAGE_TAG:-dev}"

log() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is missing. $2"
}

check_tools() {
  need docker "Install Docker (Ubuntu: sudo apt-get install docker.io, then add your user to the docker group)."
  docker info >/dev/null 2>&1 || die "The Docker daemon is not running or not reachable for this user."
  need k3d "Run: make deps"
  need kubectl "Run: make deps"
  need openssl "Install openssl (Ubuntu: sudo apt-get install openssl)."
  need curl "Install curl."
}

kc() { kubectl --context "k3d-$CLUSTER" "$@"; }
kn() { kc -n "$NS" "$@"; }

cluster_exists() { k3d cluster list -o json 2>/dev/null | grep -q "\"name\": *\"$CLUSTER\""; }

# build_service <service>: builds praxis/<service-with-dashes>:$IMAGE_TAG (Go services, or "frontend"). Sets IMAGE_NAME.
build_service() {
  case "$1" in
    database_middleware | user_service | calendar_service | mail_service | purge_job)
      IMAGE_NAME="${1//_/-}"; log "building praxis/$IMAGE_NAME"
      docker build -q -t "praxis/$IMAGE_NAME:$IMAGE_TAG" -f "services/$1/Dockerfile" . >/dev/null ;;
    frontend)
      IMAGE_NAME=frontend; log "building praxis/frontend"
      docker build -q -t "praxis/frontend:$IMAGE_TAG" -f frontend/Dockerfile frontend >/dev/null ;;
    *) die "unknown service '$1' (database_middleware|user_service|calendar_service|mail_service|purge_job|frontend)" ;;
  esac
}
