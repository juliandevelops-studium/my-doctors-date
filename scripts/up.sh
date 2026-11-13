#!/usr/bin/env bash
# Builds the images, creates the k3d cluster (if needed) and deploys everything. Safe to run again.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
check_tools

# ---------------------------------------------------------------- cluster
if cluster_exists; then
  log "cluster $CLUSTER exists"
  k3d cluster start "$CLUSTER" >/dev/null 2>&1 || true
else
  log "creating the k3d cluster $CLUSTER ($K3S_IMAGE)"
  k3d cluster create "$CLUSTER" --image "$K3S_IMAGE" --servers 1 --agents 0 \
    -p "$HTTPS_PORT:443@loadbalancer" \
    --kubeconfig-switch-context=false --wait --timeout 300s
fi
# k3s installs Traefik and its CRDs a little after the node is ready; the TLSOption in deploy/ needs the CRD.
log "waiting for Traefik"
until kc get crd tlsoptions.traefik.io >/dev/null 2>&1; do sleep 2; done
until kc -n kube-system get deploy traefik >/dev/null 2>&1; do sleep 2; done
kc -n kube-system rollout status deploy/traefik --timeout=300s >/dev/null

# ---------------------------------------------------------------- images
for svc in database_middleware user_service calendar_service mail_service frontend; do
  build_service "$svc"
done
log "building praxis/migrations"
docker build -q -t "praxis/migrations:$IMAGE_TAG" -f deploy/migrations.Dockerfile . >/dev/null
log "importing the images into the cluster"
k3d image import -c "$CLUSTER" "praxis/database-middleware:$IMAGE_TAG" "praxis/user-service:$IMAGE_TAG" \
  "praxis/calendar-service:$IMAGE_TAG" "praxis/mail-service:$IMAGE_TAG" "praxis/frontend:$IMAGE_TAG" \
  "praxis/migrations:$IMAGE_TAG" >/dev/null

# ---------------------------------------------------------------- deployment
log "namespace and secrets"
kc apply -f deploy/namespace.yaml >/dev/null
./scripts/secrets.sh

# One apply for everything. Pods that start before the database or a secret simply retry; the migration job retries
# until PostgreSQL accepts connections.
log "deploying"
kn delete job migrate --ignore-not-found >/dev/null
kc apply -f deploy/ >/dev/null
log "waiting for the database migrations"
if ! kn wait --for=condition=complete job/migrate --timeout=300s >/dev/null; then
  kn logs job/migrate --tail=50 || true
  die "the migration job failed"
fi
kn wait --for=condition=Available deploy --all --timeout=300s >/dev/null

cat <<EOF

The cluster is up.
  URL:        $BASE_URL        
  CA:         $SECRETS_DIR/ca.crt   (import it into the browser, or: curl --cacert ... )
  Admin:      $SECRETS_DIR/bootstrap-admin.txt
  Mails:      make mail-ui
  Checks:     make smoke
  Stop:       make down
EOF
