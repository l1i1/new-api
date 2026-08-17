# Console UX Reliability Technical Specification

## Existing Contracts Reused

- Channel observability continues to use the existing channel/model metrics API.
  The list trend aggregates the returned error-trend buckets; no second metrics
  store is introduced.
- Multi-key testing continues to use durable system-task results and stable
  credential IDs.
- API Info continues to use status payload fields `api_info_enabled` and
  `api_info` and the existing localized route-label helpers.
- Notices and timeline announcements continue to use the status payload and the
  persisted notification store.
- Playground stays in browser localStorage and keeps backward-compatible schema
  migration from previously stored sessions.
- Tiered price display remains a presentation concern; expression evaluation and
  settlement are unchanged.

## Channel Availability Presentation

Aggregate each channel response into chronological buckets. Each bucket exposes
`successCount`, `failureCount`, and `successRate`; unknown/empty buckets render a
neutral segment. The channel table cell renders a fixed-width segment strip and
two totals so rows cannot resize while data loads. The card renderer reuses the
same cell so default and mobile views expose the same health signal. Tag mode
requests child channel IDs and aggregates those child series for its display
row. The client splits availability requests into batches of at most 200 channel
IDs, matching the backend contract, and merges complete successful batches before
rendering. Mouse hover and keyboard focus open a tooltip with the exact bucket
range and counts. The entire channel cell is an accessible button that selects
the row and opens the existing observability dialog. The detail view follows all
API pages before calculating totals or rendering the complete result set.

## Multi-Key Result Formatting

Treat a credential's server index as optional input. The display helper accepts
only finite non-negative numbers; otherwise it uses the zero-based visible row
position. Task-result formatting includes status, HTTP status, error class/code,
and message without duplicating empty values. Persist the sanitized diagnostic
fields with credential health metadata and address task results by stable
credential ID so refreshed or reordered rows retain the correct detail. The UI
must never expose the raw credential or proxy secret.

## API Info Placement

Extract the current API Info body into a wrapper-free reusable component. The
overview removes its registry/render branch for API Info. The keys page places
the compact component in its header flow between the heading and primary action.
On narrow screens endpoints wrap vertically; copy/status controls keep stable
dimensions.

## Announcement Lifecycle

Use one announcement list containing the site notice (when non-empty) followed
by timeline announcements. Generate a stable content key from the localized raw
payload fields. Persist the latest automatically opened collection key rather
than tying dialog state to the current route component instance. Route changes
must not close an open dialog. The header bell opens the same list component from
desktop and mobile headers and marks the visible collection read. Status polling
uses the same bounded five-minute cadence as the site notice so timeline changes
are discovered in long-lived pages. A changed item produces a new collection key
and auto-opens once.

## Playground Session State

Extend the persisted session schema with mode, selected model, parameter state,
and sort position/order. Migration supplies current defaults for legacy data.
Generation state is keyed by session ID; request callbacks capture that ID and
patch only the matching session. Composer disabled state reads the active
session's request state. Session title editing uses an explicit controlled input;
search is derived state; reordering writes the complete ordered ID list.
Pointer drops choose before/after placement from the target row midpoint, while
keyboard arrows use the same explicit placement contract so moving down and
placing a session at the end remain possible.

The message editor must not replace the editable subtree on every keystroke.
Preserve the browser selection while syncing external content and only rewrite
the DOM when the source message changes outside the current edit operation.

## Pricing

For tiered-expression rows, parse the expression's display prices first, then
apply the selected group ratio through the existing price-formatting helpers.
Return both original and discounted display parts whenever the ratio is positive
and differs from one. Rendering uses the same strikethrough treatment as fixed
token pricing.

## Risks

- Metrics can contain sparse buckets; neutral rendering must not imply failure.
- A stale Playground callback can arrive after deletion; ignore updates when the
  captured session no longer exists.
- LocalStorage migrations must fail closed to defaults on malformed user data.
- Public and authenticated headers must share announcement behavior without
  mounting two competing dialogs.

## Validation Plan

1. Add focused unit/component tests before or with each bug fix.
2. Run `bun run typecheck` and focused `bunx oxlint` on changed files.
3. Run affected Vitest suites, `bun run i18n:sync`, and `bun run build`.
4. Start the local backend/frontend and perform desktop/mobile browser checks.
