export PATH := $(CURDIR)/.tools/bin:$(PATH)
OAPI_CODEGEN := github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.0
SPECTRAL     := @stoplight/spectral-cli@6.15.0
API_SERVICES := mail_service database_middleware
SERVICES := mail_service database_middleware

.PHONY: lint-api

lint-api:
	@npx --yes $(SPECTRAL) lint --ruleset .spectral.yaml --fail-severity=warn $(foreach s,$(API_SERVICES),docs/api/$(s).yaml)

migrate-test:
	@./scripts/test-migrations.sh

gen:
	@for s in $(API_SERVICES); do \
	  mkdir -p services/$$s/internal/api && \
	  go run $(OAPI_CODEGEN) -config docs/api/oapi/oapi-codegen.yaml -o services/$$s/internal/api/api.gen.go docs/api/$$s.yaml || exit 1; \
	done
	@for s in mail_service database_middleware; do \
	  mkdir -p services/$$s/internal/policy && \
	  ( cd scripts/contractcheck && go run . -root ../.. -policy $$s ) > services/$$s/internal/policy/policy.gen.go || exit 1; \
	  gofmt -w services/$$s/internal/policy/policy.gen.go; \
	done
	@for s in $(API_SERVICES); do (cd services/$$s && go mod tidy) || exit 1; done