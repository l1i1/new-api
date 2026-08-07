# Model Discovery Geo Compliance

## Goal

Apply the existing China-region pricing catalog policy consistently to user-visible group and model discovery APIs. This change hides restricted discovery data; it does not deny direct relay requests for a known model name.

## Scope

- `GET /api/user/self/groups`
- `GET /api/user/models`
- `GET /v1/models` and the Anthropic/Gemini list representations produced by `ListModels`
- `GET /v1/models/:model`
- Existing `GET /api/pricing` filtering behavior remains, using the shared compliance configuration contract

## Country Decision

Reuse the deployed request-country signals in this order:

1. A China result from either `CF-IPCountry` or `EO-Client-IPCountry`
2. MaxMind lookup using `CF-Connecting-IP`, `EO-Client-IP`, `X-Real-IP`, `X-Forwarded-For`, Gin `ClientIP`, then the TCP peer

A non-China or unrecognized country-header value does not suppress the GeoIP fallback. Forwarded headers remain trusted only because the deployment proxy chain overwrites and sanitizes them.

The supported configuration contract is:

| Option | Environment default | Default | Validation |
| --- | --- | --- | --- |
| `compliance_geoip.enabled` | `COMPLIANCE_GEOIP_ENABLED` | `true` | Boolean |
| `compliance_geoip.country_codes` | `COMPLIANCE_GEOIP_COUNTRY_CODES` | `CN` | 1-64 comma/newline-separated two-letter codes |
| `compliance_geoip.model_keywords` | `COMPLIANCE_GEOIP_MODEL_KEYWORDS` | `gpt,gemini,claude,grok` | 1-64 keywords, each 1-64 characters |
| `compliance_geoip.group_keywords` | `COMPLIANCE_GEOIP_GROUP_KEYWORDS` | `gpt,gemini,claude,grok,genpic` | 1-64 keywords, each 1-64 characters |
| `compliance_geoip.retry_backoff_minutes` | `COMPLIANCE_GEOIP_RETRY_BACKOFF_MINUTES` | `5` | Integer from 1 through 1440 |
| `compliance_geoip.db` | `COMPLIANCE_GEOIP_DB` | platform path or `GeoLite2-Country.mmdb` | Path up to 1024 characters |
| `compliance_geoip.url` | `COMPLIANCE_GEOIP_URL` | bundled HTTPS source | HTTPS URL |
| `compliance_geoip.sha256` | `COMPLIANCE_GEOIP_SHA256` | empty | Optional SHA-256 digest |

Database-backed values override environment variables when non-empty. Invalid environment values fall back to the safe defaults; invalid administrator values are rejected before persistence. List values are matched case-insensitively and normalized by the UI. The earlier pricing-specific keys are not read or migrated. Unknown country and unavailable GeoIP continue to fail open.

Forwarded-header names and trust, the HTTPS-only download rule, the 30-second download timeout, and the 100 MiB database limit are security constraints rather than administrator options.

## Filtering Contract

For requests identified as a configured country:

- Model identifiers containing any configured model keyword are omitted, case-insensitively.
- Group identifiers containing any configured group keyword are omitted, case-insensitively.
- Auto-group model discovery uses only surviving groups.
- The synthetic `auto` choice is omitted when it has no surviving configured group.
- Direct retrieval of a restricted model identifier returns the existing `model_not_found` response.

Descriptions and localized display text are not scanned. Existing authentication, user-group permission, token model-limit, billing-configuration, ordering, ownership, and response-format behavior remains authoritative.

## HTTP Contract

- Request formats, status codes, and JSON schemas do not change.
- Filtered responses include the matched lowercase country code in `X-Compliance-Filtered`.
- Region-sensitive discovery responses use private no-store cache headers.
- `/api/pricing` keeps the matched lowercase country code in `X-Pricing-Filtered` and also emits the general compliance header.
- Non-China response data remains unchanged.

## Verification

- Unit tests cover option validation, configured country/model/group matching, safe environment fallback, and stable filtering order.
- Controller tests cover China and non-China group/model discovery, Auto groups, token model limits, all list representations, direct model retrieval, headers, and empty results.
- Run `go test ./controller ./service` and `go vet ./controller ./service`.
- Run frontend i18n synchronization, type checking, focused lint, and the production build after updating the settings label.

## Risks

- This is discovery filtering, not access enforcement. Relay enforcement requires a separate policy change.
- A missing database can make the first lookup wait for the existing bounded download. Download failure remains fail-open during a five-minute retry backoff, then the same configuration is retried.
- Forwarded header trust remains dependent on the deployed proxy chain sanitizing and setting the supported headers.
