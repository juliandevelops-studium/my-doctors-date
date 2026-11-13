#!/usr/bin/env bash
# Rebuilds one service image, imports it and restarts the deployment: scripts/redeploy.sh user_service
set -euo pipefail
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
check_tools
cluster_exists || die "the cluster $CLUSTER is not running (make up)"
svc="${1:-}"
[ -n "$svc" ] || die "usage: redeploy.sh database_middleware|user_service|calendar_service|mail_service|purge_job|frontend"
build_service "$svc"
name="$IMAGE_NAME"
k3d image import -c "$CLUSTER" "praxis/$name:$IMAGE_TAG" >/dev/null
if [ "$svc" = purge_job ]; then # a CronJob: the next run uses the new image, nothing to restart
  echo "purge-job image updated; run it now with: kubectl -n $NS create job --from=cronjob/purge-job purge-now"
  exit 0
fi
kn rollout restart "deploy/$name" >/dev/null
kn rollout status "deploy/$name" --timeout=180s
