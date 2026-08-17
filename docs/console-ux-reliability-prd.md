# Console UX Reliability Improvements

## Problem

Several existing console features expose the required data but make routine work
harder than necessary. Channel health is hidden behind a dense detail table,
multi-key failures lack actionable diagnostics, API endpoint information is
separated from key creation, announcement dialogs lose state during navigation,
Playground settings leak across conversations, and tiered-expression prices do
not consistently show the undiscounted comparison price.

## Goals

- Make channel availability scannable directly from the channel list and keep
  detailed observability one click away.
- Make multi-key test results actionable and stable for every credential row.
- Put compact API endpoint information in the API key creation workflow.
- Treat site notices and timeline announcements as one durable announcement
  list that survives public-route navigation and can be reopened from the bell.
- Persist Playground mode, model, parameters, title, and ordering per session;
  isolate in-flight generation state by session.
- Show the original price beside discounted tiered-expression pricing.
- Complete user-facing translations in all seven frontend locales.

## Non-goals

- No new monitoring database, notification delivery channel, or pricing engine.
- No changes to credential secret storage or channel auto-disable policy.
- No server-side Playground conversation storage or cross-device synchronization.
- No billing formula changes.

## Acceptance Criteria

1. Channel rows show a compact bucketed availability trend, successful and
   failed request totals, and a hover/focus detail for a bucket with its time
   range, success rate, success count, and failure count in both table and card
   views, including tag-aggregated rows. Activating a channel cell opens that
   channel's observability detail. Detail totals and rows cover the complete
   result set rather than only the first API page.
2. Channel observability and multi-key controls contain no untranslated mixed
   English/Chinese labels in any supported locale.
3. Multi-key rows never render `NaN`; missing indexes degrade to a stable row
   number or fingerprint-derived identity. Failed tests show HTTP status and the
   returned error message/class when available, and those diagnostics remain
   available after the dialog is closed, reopened, or the page is refreshed.
4. `/dashboard/overview` no longer renders the API Info panel. `/keys` renders
   the same endpoint information without a card wrapper between the page title
   and the Create API Key action, with a compact responsive layout.
5. Navigating from `/` to `/pricing` cannot close an already opened announcement
   dialog. New or changed notice/timeline announcement content is discovered
   while the page remains open and auto-opens as one announcement list. The
   navigation bell opens that same list instead of the old tabbed notification
   panel on both desktop and mobile headers.
6. Every Playground session restores its own chat/image-generation mode, model,
   and parameter values. A request in session A does not disable sending or
   editing in session B, and completion updates only session A.
7. Playground attachments render above the text editor and align left. The
   image mode label is `Image generation` (localized). Editing a message keeps
   the caret at the user's edit position. Sessions support title editing,
   search, and pointer/keyboard-accessible drag ordering that persists.
8. Tiered-expression model cards show the undiscounted price struck through
   whenever the selected group ratio changes the displayed price.

## Verification

- Focused regression tests for state migration, concurrency isolation, caret
  preservation, notification navigation, multi-key formatting, and pricing.
- Frontend typecheck, lint for all changed files, affected Vitest suites, i18n
  synchronization, and production build.
- Browser screenshot checks at 1440x900 and 390x844 for channels, keys,
  announcements, Playground, and pricing, including overflow and interaction.
