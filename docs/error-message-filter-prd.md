# Relay Error Message Privacy Filter

## Problem

Some upstream channels include provider inventory, provider branding, or
internal routing metadata in an error returned to an API client. Examples
include CPA errors with a `providers=...` list and OpenCode Go errors containing
`(Console Go)`. These details are operationally useful to administrators but
are not appropriate for end users and can reveal private upstream topology.

## Goal

Remove configured sensitive fragments from relay error messages returned to API
clients while preserving the original error in server logs and error records.
Administrators must be able to enable or disable the behavior and edit the
matching expression without a code deployment.

## Scope

- OpenAI-compatible, Claude-compatible, and realtime relay error responses
  emitted by the relay controller.
- A persisted system option for the filter switch.
- A persisted Go RE2 regular expression used as a remove-only filter.
- An Operations > Error message privacy editor for administrators.

Out of scope: rewriting server logs, changing upstream requests, or applying
the filter to payment, authentication, or other non-relay API errors.

## Acceptance Criteria

1. With the default filter enabled, CPA errors do not expose the `providers=`
   inventory segment to clients.
2. With the default filter enabled, `(Console Go)` is absent from client error
   messages.
3. The original error remains available in server-side logs and error records.
4. An administrator can toggle the filter and edit the RE2 expression from
   the Operations settings page.
5. Invalid expressions are rejected without changing the active or persisted
   configuration.
6. Disabling the filter preserves the complete upstream message for clients.
7. Empty or unmatched expressions do not corrupt punctuation or request IDs.
8. Existing relay response formats and request-id behavior remain intact.

## Risks and Constraints

- The expression is administrator-controlled and must be compiled with Go's
  RE2 engine; invalid expressions must fail closed for the update request.
- Filtering happens only at the client response boundary so diagnostics remain
  actionable.
- The default expression must remove the surrounding provider metadata rather
  than leave unmatched parentheses or a dangling space.
