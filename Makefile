# API tooling is pinned and runs via npx / go run. Cluster tooling (k3d, kubectl) lives in .tools/bin.
export PATH := $(CURDIR)/.tools/bin:$(PATH)
OAPI_CODEGEN := github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.0
SPECTRAL     := @stoplight/spectral-cli@6.15.0
PRISM        := @stoplight/prism-cli@5.14.2
API_SERVICES := user_service calendar_service mail_service database_middleware purge_job
SERVICES := user_service calendar_service mail_service database_middleware
# Services that are not migrated yet are skipped automatically (Phase 6/7 add calendar_service, Phase 7 the frontend).

.PHONY: up down deps status redeploy smoke mail-ui logs build test lint lint-api gen mock check-contracts migrate-test test-db seed e2e test-frontend stop start
.DEFAULT_GOAL := build

## Platform (Phase 8): a k3d cluster with Traefik, NetworkPolicies and all services. See deploy/README.md.
deps:       ## install k3d and kubectl into .tools/bin (checksum verified, no sudo)
	@./scripts/install-tools.sh

up:         ## build the images and start the cluster (first run: a few minutes)
	@./scripts/up.sh

down:       ## delete the cluster (database content is lost; secrets/ and the local CA stay)
	@./scripts/down.sh

stop:       ## stop the cluster but keep all data (make start brings it back)
	@./scripts/stop.sh

start:      ## start a cluster that was stopped with make stop
	@./scripts/start.sh

status:     ## pods, policies and the URL
	@./scripts/status.sh

redeploy:   ## rebuild and restart one service: make redeploy S=user_service
	@./scripts/redeploy.sh $(S)

smoke:      ## end-to-end checks of the running cluster: TLS, NetworkPolicies, pod security, registration + mail
	@./scripts/smoke.sh

e2e:        ## browser-like end-to-end test of all user flows against the running cluster
	@./scripts/e2e.sh

test-frontend: ## unit and integration tests and lint of the frontend
	@cd frontend && npm ci --ignore-scripts && npm test && npx eslint .

mail-ui:    ## open the Mailpit UI (all mails of the demo) on http://127.0.0.1:8025
	@. scripts/lib.sh && echo "Mailpit: http://127.0.0.1:8025 (Ctrl-C to stop)" && kn port-forward svc/mailpit 8025:8025

logs:       ## follow the logs of all services (make logs S=user-service for one)
	@. scripts/lib.sh && kn logs -f --tail=50 --prefix $(if $(S),deploy/$(S),-l 'app in (database-middleware,user-service,calendar-service,mail-service)' --max-log-requests 8)

build:
	@for s in $(SERVICES); do [ -d services/$$s ] || continue; (cd services/$$s && go build ./...) || exit 1; done

test:
	@for s in $(SERVICES); do [ -d services/$$s ] || continue; (cd services/$$s && go test ./...) || exit 1; done

lint:
	@for s in $(SERVICES); do [ -d services/$$s ] || continue; (cd services/$$s && golangci-lint run ./...) || exit 1; done
	@cd frontend && npm run lint --if-present

lint-api:
	@npx --yes $(SPECTRAL) lint --ruleset .spectral.yaml --fail-severity=warn $(foreach s,$(API_SERVICES),docs/api/$(s).yaml)

check-contracts:
	@cd scripts/contractcheck && go run . -root ../..

gen:
	@for s in $(API_SERVICES); do \
	  [ -d services/$$s ] || continue; \
	  mkdir -p services/$$s/internal/api && \
	  go run $(OAPI_CODEGEN) -config docs/api/oapi/oapi-codegen.yaml -o services/$$s/internal/api/api.gen.go docs/api/$$s.yaml || exit 1; \
	done
	@for s in user_service calendar_service; do \
	  [ -d services/$$s ] || continue; \
	  mkdir -p services/$$s/internal/mwclient services/$$s/internal/mailclient services/$$s/internal/policy && \
	  ( cd scripts/contractcheck && go run . -root ../.. -client-config $$s ) > .gen-client-$$s.yaml || exit 1; \
	  go run $(OAPI_CODEGEN) -config .gen-client-$$s.yaml -o services/$$s/internal/mwclient/client.gen.go docs/api/database_middleware.yaml || exit 1; \
	  go run $(OAPI_CODEGEN) -config docs/api/oapi/oapi-codegen-mail-client.yaml -o services/$$s/internal/mailclient/client.gen.go docs/api/mail_service.yaml || exit 1; \
	  rm -f .gen-client-$$s.yaml; \
	  ( cd scripts/contractcheck && go run . -root ../.. -policy $$s ) > services/$$s/internal/policy/policy.gen.go || exit 1; \
	  gofmt -w services/$$s/internal/policy/policy.gen.go; \
	done
	@for s in purge_job; do \
	  [ -d services/$$s ] || continue; \
	  mkdir -p services/$$s/internal/mwclient && \
	  ( cd scripts/contractcheck && go run . -root ../.. -client-config $$s ) > .gen-client-$$s.yaml || exit 1; \
	  go run $(OAPI_CODEGEN) -config .gen-client-$$s.yaml -o services/$$s/internal/mwclient/client.gen.go docs/api/database_middleware.yaml || exit 1; \
	  rm -f .gen-client-$$s.yaml; \
	done
	@for s in mail_service database_middleware; do \
	  mkdir -p services/$$s/internal/policy && \
	  ( cd scripts/contractcheck && go run . -root ../.. -policy $$s ) > services/$$s/internal/policy/policy.gen.go || exit 1; \
	  gofmt -w services/$$s/internal/policy/policy.gen.go; \
	done
	@for s in $(API_SERVICES) purge_job; do [ -d services/$$s ] || continue; (cd services/$$s && go mod tidy) || exit 1; done

mock:
	@echo "Prism mocks: user 4011, calendar 4012, mail 4013, middleware 4014 (Ctrl-C stops all)"
	@trap 'kill 0' INT TERM; \
	i=1; for s in $(API_SERVICES); do \
	  npx --yes $(PRISM) mock -h 127.0.0.1 -p 401$$i docs/api/$$s.yaml & i=$$((i+1)); \
	done; wait

migrate-test:
	@./scripts/test-migrations.sh

test-db:
	@./scripts/test-db.sh

seed:        ## fill the running cluster's database with demo content (make seed)
	@./scripts/seed.sh
