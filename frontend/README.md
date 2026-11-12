# frontend_provider

Server-rendered web UI (Express 5, Nunjucks, Helmet) in German. It has **no business logic and no authorisation**: it
shows forms, forwards them to `user_service` and `calendar_service` with the user's JWT and renders the answers. Every
rule (roles, ownership, password policy, lock-out, booking conflicts) is enforced by the backend; hiding a menu entry
is only convenience (`showOnlyFor`). Decisions: `docs/decisions.md` 47 to 54.

## Design in short

| Topic | Choice |
|-|-|
| JavaScript in the browser | None. Pages work with plain forms and links, so the CSP can be `default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`. The QR code of the MFA setup is an inline SVG made on the server. |
| Session | The 15-minute user JWT lives in the cookie `__Host-session` (HttpOnly, Secure, SameSite=Strict, no Domain). There is no refresh token: after 15 minutes the user logs in again. |
| Login steps | Password -> `__Host-mfa` (challenge token, 5 minutes, same flags) -> code or recovery code. Accounts without a second factor get a `setup` token and see only `/setup`. |
| CSRF | Double submit: cookie `__Host-csrf` plus an HMAC-bound form field `_csrf` in every POST form, additionally `Origin` / `Sec-Fetch-Site` must be same-origin. The token is rotated after login and logout. |
| Links in mails | Open a page with a button; only the POST changes state (mail scanners that open links do not confirm anything). |
| Input | IDs are checked as UUID before they are put into a backend path; `next=` accepts local paths only; forms are validated again by the backend. |
| Output | Nunjucks autoescape, no `safe` filter, `Cache-Control: no-store`, no ETag, logs never contain query strings, tokens or form values. |

## Run and test

```sh
cd frontend
npm ci --ignore-scripts
npm test              # 105 unit and integration tests against a fake backend (no cluster needed)
npx eslint .
make e2e              # from the repository root: 35 end-to-end tests against the running cluster
```

`make e2e` (`scripts/e2e.sh`) port-forwards Mailpit, talks HTTPS to Traefik with the local CA and follows every flow like a
browser: UF-01 to UF-13 (except the final purge), the roles, the lock-out, CSRF, XSS, session revocation and the rate limit.
It uses unique names per run, so it can run again on the same cluster. The bootstrap administrator can be used only
once; the new password and TOTP key of that account are kept in `secrets/e2e-admin.json` (gitignored). After `make down`
that file is useless: delete it (`make down` does this when the whole cluster is removed).

## What is tested, what is not

| Area | Status |
|-|-|
| UF-01 to UF-12, roles, lock-out, CSRF, XSS, cookies, revocation, rate limit, HTTP -> HTTPS | End to end in the cluster (`make e2e`), plus integration tests |
| UF-13 stage 1 (request deletion: account closed, future bookings cancelled, mail) | End to end |
| UF-13 stage 2 (hard delete after the retention period) and `bookingsCompletePast` | **Not yet**: needs the purge job (Phase 5). Backend operations are tested in `make test-db`. |
| Booking reminder mails | **Not yet**: no reminder job. The template exists. |
| Browsers | Not tested with a real browser (no JavaScript is used, so differences are small). Accessibility was checked by reading the HTML only (labels, headings, skip link, `aria-current`). |
| Ubuntu host | Not tried. Only macOS with Docker Desktop and k3d so far. |
| CI pipeline | Postponed on purpose. |

A revoked token can keep working for up to 10 seconds, because the backends cache a positive token check for that long
(`api/internal.md` section 1). The end-to-end test waits for this window.
