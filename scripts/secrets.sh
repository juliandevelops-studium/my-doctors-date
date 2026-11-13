#!/usr/bin/env bash
# Creates the secrets and the generated configuration of the cluster. Nothing here is committed: random values are
# created at run time and live only in Kubernetes Secrets and in the ignored directory secrets/.
# Existing secrets are never replaced, so running `make up` again does not log everybody out or lock the database.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
umask 077
mkdir -p "$SECRETS_DIR"
chmod 700 "$SECRETS_DIR"

have() { kn get "$1" "$2" >/dev/null 2>&1; }
rand_hex() { openssl rand -hex "$1"; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# ---------------------------------------------------------------- generated configuration
kn create configmap praxis-config \
  --from-literal=PUBLIC_BASE_URL="$BASE_URL" \
  --from-literal=PRACTICE_NAME="${PRAXIS_NAME:-Arztpraxis Muster}" \
  --dry-run=client -o yaml | kn apply -f - >/dev/null

# ---------------------------------------------------------------- TLS of the ingress: local CA (kept) + leaf (per cluster)
if [ ! -f "$SECRETS_DIR/ca.key" ]; then
  log "creating the local CA (secrets/ca.crt); trust it in your browser once"
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$SECRETS_DIR/ca.key" 2>/dev/null
  openssl req -x509 -new -key "$SECRETS_DIR/ca.key" -sha256 -days 3650 -subj "/CN=Praxis local CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -out "$SECRETS_DIR/ca.crt"
fi

# leaf <dir> <ca.crt> <ca.key> <cn> <days> <san>
leaf() {
  local dir="$1" cacrt="$2" cakey="$3" cn="$4" days="$5" san="$6"
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$dir/tls.key" 2>/dev/null
  openssl req -new -key "$dir/tls.key" -subj "/CN=$cn" -out "$dir/tls.csr"
  printf 'subjectAltName=%s\nbasicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\n' "$san" >"$dir/ext.cnf"
  openssl x509 -req -in "$dir/tls.csr" -CA "$cacrt" -CAkey "$cakey" -set_serial "0x$(rand_hex 12)" -days "$days" -sha256 \
    -extfile "$dir/ext.cnf" -out "$dir/tls.crt" 2>/dev/null
}

if ! have secret praxis-tls; then
  log "creating the TLS certificate of $HOST"
  mkdir "$work/ingress"
  leaf "$work/ingress" "$SECRETS_DIR/ca.crt" "$SECRETS_DIR/ca.key" "$HOST" 365 "DNS:$HOST,DNS:localhost,IP:127.0.0.1"
  kn create secret tls praxis-tls --cert="$work/ingress/tls.crt" --key="$work/ingress/tls.key" >/dev/null
fi

# ---------------------------------------------------------------- PostgreSQL: internal CA (key is destroyed after signing)
if ! have secret postgres-credentials; then
  log "creating the database credentials and its TLS certificate"
  su_pw="$(rand_hex 24)"; owner_pw="$(rand_hex 24)"; app_pw="$(rand_hex 24)"
  mkdir "$work/pg"
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$work/pg/ca.key" 2>/dev/null
  openssl req -x509 -new -key "$work/pg/ca.key" -sha256 -days 825 -subj "/CN=Praxis database CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" -addext "keyUsage=critical,keyCertSign" -out "$work/pg/ca.crt"
  leaf "$work/pg" "$work/pg/ca.crt" "$work/pg/ca.key" postgres 825 \
    "DNS:postgres,DNS:postgres.$NS.svc,DNS:postgres.$NS.svc.cluster.local,DNS:postgres-0.postgres.$NS.svc.cluster.local"
  kn create secret generic postgres-tls --from-file=tls.crt="$work/pg/tls.crt" --from-file=tls.key="$work/pg/tls.key" >/dev/null
  kn create configmap postgres-ca --from-file=ca.crt="$work/pg/ca.crt" --dry-run=client -o yaml | kn apply -f - >/dev/null
  kn create secret generic postgres-credentials --from-literal=superuser-password="$su_pw" \
    --from-literal=owner-password="$owner_pw" --from-literal=app-password="$app_pw" >/dev/null
  dsn() { printf 'postgres://%s:%s@postgres.%s.svc.cluster.local:5432/praxis?sslmode=verify-full&sslrootcert=/etc/praxis/postgres-ca/ca.crt' "$1" "$2" "$NS"; }
  kn create secret generic middleware-db --from-literal=url="$(dsn praxis_app "$app_pw")" >/dev/null
  kn create secret generic migrate-db --from-literal=url="$(dsn praxis_owner "$owner_pw")" >/dev/null
  rm -f "$work/pg/ca.key" # nobody can issue certificates for the database any more
fi

# ---------------------------------------------------------------- signing keys (Ed25519)
genkey() { # secret-name
  if ! have secret "$1"; then
    log "creating the signing key $1"
    openssl genpkey -algorithm ed25519 -out "$work/$1.pem" 2>/dev/null
    kn create secret generic "$1" --from-file=key.pem="$work/$1.pem" >/dev/null
  fi
}
genkey jwt-key
genkey service-key-user-service
genkey service-key-calendar-service
genkey service-key-purge-job

# The public keys of the callers are not secret: database_middleware and mail_service verify service tokens with them.
pub() { kn get secret "$1" -o 'go-template={{index .data "key.pem"}}' | base64 -d | openssl pkey -pubout; }
pub service-key-user-service >"$work/user_service.pub"
pub service-key-calendar-service >"$work/calendar_service.pub"
pub service-key-purge-job >"$work/purge_job.pub"
kn create configmap service-keys --from-file=user_service.pub="$work/user_service.pub" \
  --from-file=calendar_service.pub="$work/calendar_service.pub" \
  --from-file=purge_job.pub="$work/purge_job.pub" --dry-run=client -o yaml | kn apply -f - >/dev/null

if ! have secret mfa-key; then
  log "creating the MFA key"
  openssl rand -base64 32 | tr -d '\n' >"$work/mfa.key"
  kn create secret generic mfa-key --from-file=key="$work/mfa.key" >/dev/null
fi

# ---------------------------------------------------------------- key of the frontend for the CSRF tokens
if ! have secret frontend-secret; then
  log "creating the frontend secret"
  openssl rand -base64 48 | tr -d '\n' >"$work/session.key"
  kn create secret generic frontend-secret --from-file=key="$work/session.key" >/dev/null
fi

# ---------------------------------------------------------------- bootstrap administrator (UF-03)
if ! have secret bootstrap-admin; then
  email="admin@$HOST"
  pw="$(openssl rand -base64 36 | tr -d '/+=\n' | head -c 24)"
  kn create secret generic bootstrap-admin --from-literal=email="$email" --from-literal=password="$pw" >/dev/null
  {
    echo "Bootstrap administrator (first login: change the password and set up MFA)"
    echo "URL:      $BASE_URL"
    echo "E-Mail:   $email"
    echo "Password: $pw"
  } >"$SECRETS_DIR/bootstrap-admin.txt"
  chmod 600 "$SECRETS_DIR/bootstrap-admin.txt"
  log "bootstrap administrator: see secrets/bootstrap-admin.txt"
fi
