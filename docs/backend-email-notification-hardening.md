# Backend Email and Notification Hardening

## Objective

Localize backend email and operational notifications for every supported UI
language while preserving existing delivery contracts and preventing dynamic
values from being interpreted as email HTML.

## Scope

- Verification, password-reset, invoice-status, quota, channel-state,
  channel-test, and upstream-model notifications.
- Backend locales: English, Simplified Chinese, Traditional Chinese, French,
  Russian, Japanese, and Vietnamese.
- Email, webhook, Bark, and Gotify delivery.
- A shared branded HTML email frame using inline styles.

No database migration, frontend contract, notification rate-limit behavior, or
deployment workflow change is included.

## Security And Compatibility Constraints

- Locale HTML is trusted source-controlled markup. Dynamic template data must
  be rendered with `html/template` before it reaches the branded frame.
- Safe rendered email bodies use the explicit `template.HTML` type so new call
  sites cannot accidentally pass an interpolated string without an intentional
  trust conversion.
- Password-reset and notification links rely on `html/template` URL-context
  filtering; the frame footer additionally accepts only absolute HTTP(S) URLs.
- Legacy notification `Values` keep sequential `{{value}}` replacement.
- Legacy webhook `Values` keep the historical repeated `fmt.Sprintf` behavior
  for `%s`, `%v`, and related formatting directives.
- Structured `TemplateData` uses `{{.field}}` rendering for every backend.
- The original fixed-content `NotifyUpstreamModelUpdateWatchers` function and
  the serialized `values` field remain source- and JSON-compatible.
- Legacy email content is parsed with separate delimiters so unrelated literal
  `{{...}}` text is not interpreted as a Go template.

## Acceptance Criteria

1. Dynamic text, attribute, and URL values cannot inject executable email HTML.
2. Unsafe URL schemes are rendered inert by the HTML template engine.
3. Webhook callers using legacy `Values` and printf directives retain output
   compatibility.
4. All seven locale files have identical key sets and initialize successfully.
5. Verification uses request language; account-bound mail uses saved language.
6. Email, webhook, Bark, and Gotify receive the expected localized content.
7. Focused tests, builds, vet, formatting checks, and release workflow gates
   pass before production rollout.

## Verification And Rollout

- Run focused controller, i18n, and service tests.
- Run root and standalone `relaykit` tests/builds plus root `go vet ./...`.
- Run `git diff --check` and the deployment transaction tests.
- Merge the reviewed commit into `tokeness/main`.
- Publish with `Dockerfile.tokeness` and deploy only the resulting immutable
  GHCR digest through the staged four-node production workflow.
