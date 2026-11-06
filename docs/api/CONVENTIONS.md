# API conventions

Apply to `user_service.yaml`, `calendar_service.yaml`, `mail_service.yaml` and `database_middleware.yaml`. Change the spec before the code (`docs/decisions.md`, decision 17).

## Files

| File | Purpose |
|-|-|
| `api/<service>.yaml` | OpenAPI **3.0.3** (decision 8), one file per service, self-contained |
| `docs/api/authorization.yaml` | Every route with allowed roles, token scope and ownership rule. Source of `docs/authz-matrix.md` |
| `docs/api/internal.md` | User JWT, JWKS, service tokens, middleware operations and their allow lists |
| `.spectral.yaml` | Lint rules (`make lint-api`) |

## General rules

- All paths start with `/v1`. Operational routes (`/healthz`) are the only exception.
- JSON only (`application/json`), UTF-8. Requests with another content type get `415`.
- Property names are `snake_case`. Enum values are lower-case ASCII (`bestaetigt`, `storniert`).
- IDs are UUIDs (`format: uuid`). Timestamps are RFC 3339 in UTC (`2030-01-01T09:00:00Z`), dates are `YYYY-MM-DD`, times of day are `HH:MM`.
- `operationId` is unique across all four files, `lowerCamelCase`, and the key into `authz.yaml`.
- Every request body schema sets `additionalProperties: false`. Unknown fields give `422`.
- Every string has `maxLength`, every array `maxItems`, every number `minimum`/`maximum`. Limits are defined once in the schema, not in handlers. The limits match the database `CHECK` constraints (`docs/data-model.md`).
- Request bodies are limited to 64 KiB (the service import to 256 KiB).

## Identity and authorisation

- The caller's identity and roles come **only** from the validated token (SR.03). Bodies, query strings and paths never carry the identity of the caller. There is no `patient_id` in a patient's request body.
- Exception: staff (`desk`, `organisator`) book for a patient and pass `patient_id`. For the role `patient` the field is ignored.
- Deny by default: a route that is not in `authz.yaml` is not registered.
- Order of checks: `401` (token) -> `403` (role) -> `422` (validation) -> `404` (object missing or not owned) -> `409` (state conflict).
- Ownership is enforced in the database query (filter on `patient_id` or `doctor_id`), not after loading the object.

## Status codes

| Code | Use |
|-|-|
| `200`, `201`, `204` | Success. `201` with `Location` for created objects. `204` without body. |
| `400` | Malformed JSON or malformed query/path parameter |
| `401` | No token, invalid or expired token, or `scope=setup` token on a full route |
| `403` | Authenticated, but the role is not allowed on this route |
| `404` | Object does not exist **or belongs to someone else** (never `403` for foreign objects) |
| `409` | Conflict with current state: slot taken (`23P01`), e-mail already used by admin create, booking already cancelled |
| `415` | Wrong content type |
| `422` | Semantically invalid input: password policy, length, enum, `systemadmin` combined with another role |
| `429` | Rate limit |
| `5xx` | Internal error. Body never contains stack traces, SQL or internal hostnames. |

## Errors

Errors are `application/problem+json` (RFC 9457):

```json
{ "type": "about:blank", "title": "Slot already taken", "status": 409, "code": "slot_taken", "detail": "..." }
```

- `code` is a stable machine-readable string (`slot_taken`, `password_too_weak`, `validation_failed`, ...).
- For `422`, `errors` lists `{ "field": "...", "code": "..." }` entries. Messages never echo secrets.
- Login, registration and password reset give neutral answers (UF-01, UF-02, UF-12): a wrong password, an unknown e-mail, a locked and a disabled account all return the same `401` `invalid_credentials`. A registration with a known e-mail returns the same `202` as a new one.

## Pagination and filtering

- Lists use `limit` (default 50, max 200) and `offset` (default 0). The response is `{ "items": [...], "total": n }`.
- Date range queries use `from` and `to` (RFC 3339). The maximum range is 62 days.
- Sorting is fixed per route (documented in the spec), no free `sort` parameter.

## Soft delete

Soft-deleted records (`deleted_at IS NOT NULL`) are invisible to every route. The filter lives centrally in `database_middleware`, not in the services (Löschkonzept).

## Mocks and generation

- `make lint-api` runs Spectral against `api/*.yaml`.
- `make gen` runs `oapi-codegen` and writes `services/<service>/internal/api/api.gen.go` (types, chi server interface, strict handlers).
- `make mock` starts Prism mocks on ports 4011 (user), 4012 (calendar), 4013 (mail) and 4014 (middleware).
- `make check-contracts` runs `scripts/contractcheck` (specs vs. `authz.yaml`, allow lists, UF coverage) and regenerates `docs/authz-matrix.md`.
