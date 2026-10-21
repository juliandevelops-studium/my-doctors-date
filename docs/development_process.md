# Development Process

This document describes the development process.

1. Project initialisation
2. Definition of the global service's APIs in `authorization.yaml`.

## Tools

### Spectral for OpenAPI Linting

> See [definitions](/.spectral.yaml).

Custom rules: 

* `path-version-prefix`
  * Purpose: All API paths must be versioned.
  * A path is valid if it starts with `/v1/`, or if it is exactly `/healthz`.

* `operation-id-lower-camel-case`
  * Purpose: Enforce a consistent naming style for operation IDs.
  * Only allow camel case ids

### oapi-codegen

Generated Go code based on OpenAPI specifications.
The tool is configured in:

* [oapi-codegen.yaml](/docs/api/oapi/oapi-codegen.yaml)

## Mail Service

The mail service's API is defined in [mail_service.yaml](/docs/api/mail_service.yaml).