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
- Existing billing and payment behavior is authoritative. The historical
  client-side recharge formula must not be ported. The historical 20%
  first-top-up affiliate promise is shown only because the native backend now
  settles that reward through its database-backed invite reward ledger.

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

The legacy Tokeness implementation in
`tools/nginx-dev-proxy/repo-newapi-webdist/static/custom/js/home.js` is the
visual and content source of truth for the native home page. Native migration
replaces the injection mechanism, not the design. The React implementation
must preserve:

- the 1260 px grid-backed canvas and sharp 2 px / 0 px geometry;
- the split hero with its integration-steps rail;
- the four-cell capability matrix;
- the `30+ providers / 1 gateway / 3 layers` system band;
- the provider icon wall in its reviewed order;
- the routing list, compatibility specification table, Tokeness footer, legal
  links, contact link, LM Speed verification badge, and linked New API project
  attribution beside the Tokeness copyright;
- the legacy six-language copy and responsive collapse behavior at 980 px,
  720 px, and 520 px.

The implementation may replace inline SVG markup with the already-installed
`@lobehub/icons` components and must use React/i18next instead of DOM mutation,
global observers, or injected styles. Those substitutions must not alter the
visible information architecture. The home route renders this Tokeness footer
instead of appending the generic public footer a second time.

The historical production value `<home-tokeness/>` is a legacy injector
sentinel, not administrator-authored HTML. Back it up and clear it from
`HomePageContent` before removing the injector. Do not add permanent client-side
handling for that sentinel or weaken the custom HTML/Markdown/URL contract.

Acceptance requires desktop and 390 px browser screenshots. At both sizes the
page must have no horizontal overflow, hidden sections, clipped text, or
unreachable navigation. A generic replacement landing page does not satisfy
the migration even if it contains equivalent product claims.

## Wallet Decisions

- Continue to display the server-calculated payment amount. Do not reconstruct
  it from `amount * exchangeRate`; server calculation includes group ratios,
  discounts, and payment-specific rules.
- Present the custom amount row as the reviewed three-part relationship:
  amount input, the configured base price multiplier, and the server-calculated
  payable amount. The multiplier is explanatory only; it must never replace the
  payable amount returned by the backend.
- Format payment amounts with the existing local-currency formatter.
- For the standard EPay flow, render an explicit localized reminder to keep the
  payment page open until it redirects back. Keep the payable amount visible.
- For non-EPay flows, omit the `You Pay` row from the confirmation dialog. Those
  providers present and settle their payable amount in their own checkout.
- Preserve the historical affiliate promise and referral-link flow, then extend
  the card with ledger-backed first-top-up completion count, applied reward
  total, pending count, and recent reward status. Continue to omit the legacy
  `aff_quota` balance and transfer-to-balance action because native rewards enter
  ordinary quota directly. Keep the displayed rate synchronized with the native
  settlement policy documented in `invite-first-topup-reward.md`.
- Keep the referral surface as one compact card using existing theme tokens and
  dividers instead of nested cards. Reward amounts are fixed to CNY like other
  wallet payment surfaces. Loading, empty, and error states must not disable or
  hide referral-link copying.

## Home Metadata

- The localized title, description, and keywords from the historical Tokeness
  home are the canonical SEO copy for `zh`, `en`, `fr`, `ru`, `ja`, and `vi`.
  `zh-TW` intentionally uses the reviewed Chinese home metadata, matching the
  legacy home contract.
- The home route owns `document.title`, primary metadata, Open Graph title and
  description, and Twitter title and description while mounted, then restores
  the previous route metadata on navigation.
- `web/index.html` carries the English Tokeness values as the crawler and
  no-JavaScript fallback. It must not ship the upstream `New API` defaults as
  the page title or SEO copy, while its generator attribution remains present.

## Theme Defaults

- New clients default to the `sunset-glow` color preset and the explicit
  `none` radius selection.
- Existing valid theme cookies remain authoritative. Changing the defaults must
  not overwrite a user's persisted preset or radius.
- Resetting theme customization returns to `sunset-glow` and `none`.

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

For the `<home-tokeness/>` sentinel, verify its exact value, back up the option,
and clear it transactionally before removing the injector from the shared
Tokeness HTML sub-filter. Preserve the title, metadata, favicon, analytics, and
language-aware HTML replacements.
