# My Doctors Date

> Dokumentation findet sich [hier](/docs/README.md).

A doctor and clinic appointment system developed with security in mind.

## Prerequisites

| Tool | Why | Notes |
| --- | --- | --- |
| Docker | builds the images and runs the cluster | must be running (`docker info` works) |
| Go | `make seed`, tests, code generation | version from `go.work` |
| Node.js >= 26 | frontend tests (`make test-frontend`) | not needed just to run the system |
| openssl, curl | secret and certificate generation | preinstalled on macOS and most Linux distributions |

`k3d` and `kubectl` are installed locally by the project (no sudo), see step 1.

## First run

All commands are run from the repository root.

1. **Install the cluster tools** (k3d and kubectl, checksum verified, into `.tools/bin`):

   ```sh
   make deps
   ```

2. **Build and start everything.** The first run takes a few minutes (images are built, the cluster is created, the database
   migrations run):

   ```sh
   make up
   ```

   This also creates `secrets/` (git-ignored) with a local CA and a bootstrap administrator account. Running `make up` again is safe
   and keeps existing secrets and data.

3. **Trust the local CA** so the browser accepts the TLS certificate. Import `secrets/ca.crt` into your browser or system keychain.

4. **Fill the database with demo data** (specialties, services, staff, doctors, patients, bookings). It is safe to run twice:

   ```sh
   make seed
   ```

5. **Open the application** at <https://praxis.localhost:8443>.

## Logging in

| Account | Login | Password |
| --- | --- | --- |
| Bootstrap administrator | see `secrets/bootstrap-admin.txt` | see `secrets/bootstrap-admin.txt` |
| Demo users (after `make seed`) | e.g. `organisator@demo.example.test`, `desk@demo.example.test`, `anna.berger@demo.example.test` | `Demo-Passwort-2026!` (or `DEMO_PASSWORD`, see below) |

- Every account has to **set up MFA at the first login** (use any TOTP app). The bootstrap administrator also has to change the
  password.
- To use another demo password: `DEMO_PASSWORD='your-long-password' make seed` (at least 15 characters, must pass the password
  policy).
- The roles are `patient`, `doctor`, `desk`, `organisator` and `systemadmin`. The list of all demo users is in
  [`services/user_service/cmd/seed/main.go`](services/user_service/cmd/seed/main.go).

### Mails

The system sends mails (registration, confirmations). In the demo they are caught by Mailpit. Open it with:

```sh
make mail-ui     # http://127.0.0.1:8025, stop with Ctrl-C
```

## Everyday use

```sh
make logs                       # follow the logs of all services (S=user-service for one)
make redeploy S=user_service    # rebuild and restart one service after a code change
make smoke
make e2e
```

Stop or remove the cluster directly with k3d (the cluster is called `praxis`):

```sh
k3d cluster stop praxis         # keep all data, bring it back with: make up
k3d cluster delete praxis       # delete the cluster and its database; secrets/ and the local CA stay
```

## Development

```sh
make build              # go build for all services
make test               # go test for all services
make lint               # golangci-lint and frontend lint
make test-frontend      # frontend unit and integration tests
make gen                # regenerate API code from docs/api/*.yaml
make check-contracts    # verify the API contracts
make mock               # Prism mock servers for the APIs
```
