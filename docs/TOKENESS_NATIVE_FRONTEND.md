# Tokeness Native Frontend Integration

## Objective

Move the Tokeness home page, administrator-authored content localization, and
valid wallet presentation changes into the canonical React frontend. Production
HTML must no longer depend on CDN-hosted scripts or global DOM/network hooks.

## Constraints

- The implementation lives in `web/` and ships inside `Dockerfile.tokeness`.
- Preserve the `/` route, public layout, authentication flows, custom home page
  override, legal routes, responsive behavior, dark mode, and upstream project
  attribution.
- Do not mutate arbitrary API responses, patch `fetch`/XHR/history, scan the
  entire DOM, or identify components by translated display text.
- Keep administrator editors backed by raw source content. Localization occurs
  only at explicit display boundaries.
- Existing billing, payment, and affiliate behavior is authoritative. The old
  injection's fixed-rate recharge formula and first-top-up affiliate promise do
  not match the current backend and must not be ported.

## Content Localization Contract

The canonical authoring syntax is:

```html
<tnt l="zh">中文内容</tnt><tnt l="en">English content</tnt>
```

Rules:

- Supported content languages are `zh`, `en`, `fr`, `ru`, `ja`, and `vi`.
- Regional language values resolve to their supported base language. Chinese
  variants, including `zh-TW`, resolve to `zh` for authored content.
- A translation group is one or more adjacent `<tnt>` elements. Whitespace may
  appear between elements, but other text or elements start a new group.
- Selection order is the active language, then `en`, then `zh`.
- Multiple groups and ordinary text may coexist in one string.
- Nested `<tnt>` elements, duplicate languages in one group, missing `l`, and
  unsupported language values are invalid. Invalid input is preserved and must
  never crash the page.
- Resolve content before URL/HTML/Markdown classification and before existing
  sanitization. URL values still have to pass the existing HTTP(S) validator.
- JSON objects with language keys are no longer an implicit localization
  format.

Initial display boundaries:

- Home page content, About, user agreement, and privacy policy
- Notice and announcement content, extra text, and dashboard previews
- Footer HTML
- FAQ questions and answers, API information descriptions
- Public and read-only model descriptions
- Wallet payment method names, product names, and subscription plan copy
- Chat preset names
- API key and playground group descriptions from `UserUsableGroups`

Unread fingerprints and editor values remain based on raw source text so a
language switch cannot create a false unread state or overwrite translations.
Group IDs, chat URLs, payment identifiers, and request payload values also
remain raw. Only human-readable labels and descriptions are localized.

## Native Home Page

The default home page keeps the existing React feature structure and replaces
generic copy with the Tokeness content hierarchy from the historical production
page:

1. One entry for all models
2. Three-step integration flow
3. Key, quota, routing, and usage controls
4. Traceable request path and compatible output
5. Provider coverage and clear calls to dashboard/pricing

Use the existing public header, footer, routing, status configuration, theme
tokens, model icon package, and motion utilities. Do not copy the historical
inline HTML, inline provider SVGs, global radius override, or DOM lifecycle
code.

## Wallet Decisions

- Continue to display the server-calculated payment amount. Do not reconstruct
  it from `amount * exchangeRate`; server calculation includes group ratios,
  discounts, and payment-specific rules.
- Format payment amounts with the existing local-currency formatter.
- For the standard EPay flow, render an explicit localized reminder to keep the
  payment page open until it redirects back. Keep the payable amount visible.
- Keep the native affiliate explanation, all three statistics, compliance
  handling, and transfer-to-balance action. The historical 20% first-top-up
  statement and hidden transfer action are not supported by current backend
  behavior.

## Data Migration

Before removing the legacy injector:

1. Inventory persisted `<tokeness-text>` values in options and announcement
   JSON without logging their content.
2. Back up affected rows to an ignored operational artifact.
3. Convert valid `<tokeness-text lang="...">` pairs to `<tnt l="...">` in a
   transaction.
4. Verify zero legacy tags remain in production-authored content.

The application does not retain indefinite compatibility with the legacy tag.

## Verification

- Unit tests cover language normalization, grouping, fallback, mixed content,
  malformed input, HTML/Markdown payloads, and idempotence.
- Component and integration tests cover reactive language switching, resolved
  URL/HTML classification, announcement previews, footer HTML, localized
  subscription sorting, wallet EPay behavior, and the mobile public-header
  language control.
- Pure logic contract tests cover chat preset labels, model-group descriptions,
  and API key group filtering. Their component wiring is covered by browser
  acceptance rather than these helper tests.
- DOMPurify sanitization remains enforced by the production `HtmlContent` and
  Markdown boundaries. Security acceptance must run in a real browser because
  the test suite's Happy DOM implementation is not a security-equivalent DOM
  for DOMPurify.
- Reloading a cached `zhTW` preference must preserve Traditional Chinese, and
  `<html lang>` must always contain a valid BCP-47 language tag.
- Leaving the home route must restore both the document title and
  `meta[name="title"]` to the configured system name.
- `bun run typecheck`, affected tests, changed-file lint and format checks,
  i18n sync check, and the production build pass. Whole-repository lint,
  format, and copyright checks still report pre-existing upstream baseline
  issues outside this change set.
- Browser checks cover desktop/mobile, light/dark, authenticated/anonymous home,
  all dynamic content surfaces, wallet payment confirmation, language switching
  without a reload, and removal of scripts and event-handler attributes from
  administrator-authored HTML.
- Built and public HTML contain no `newapi-webdist` custom script or stylesheet.

## Rollout And Rollback

Publish a reviewed `tokeness/main` image and deploy its immutable digest in the
existing order: JP-N2, EV-JP, JP-M, then EV-JP2. The forced command must reject
mutable current and target images, verify the final runtime image and health,
and restore the previous digest after failures, cancellation, or ambiguous SSH
results.

Keep the legacy injector active during the image rollout. Once all native
display boundaries are healthy, back up and transactionally migrate persisted
legacy tags. Remove only the `newapi-webdist` CSS/JavaScript injection and
`/static` CDN rewrites after the migrated content passes fixed-node and public
CDN checks. Retain the previous image digest, Nginx backup, and data backup until
public verification completes.
