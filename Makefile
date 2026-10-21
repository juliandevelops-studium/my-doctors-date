export PATH := $(CURDIR)/.tools/bin:$(PATH)
OAPI_CODEGEN := github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.0
SPECTRAL     := @stoplight/spectral-cli@6.15.0
API_SERVICES := mail_service
SERVICES := mail_service

.PHONY: lint-api

lint-api:
	@npx --yes $(SPECTRAL) lint --ruleset .spectral.yaml --fail-severity=warn $(foreach s,$(API_SERVICES),docs/api/$(s).yaml)
