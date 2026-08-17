# Navigation Customization Tech Spec

## Contracts

`HeaderNavModules` remains a JSON option. Existing boolean and object fields
continue to parse. New header entries are represented by an ordered list with
`id`, `title`, `url`, `newTab`, and `visible` fields; built-in entries retain
stable IDs and Home is always rendered first.

`SidebarModulesAdmin` remains a JSON option. Existing section/module booleans
continue to parse. JSON object key order represents the section and module
order; known entries missing from older settings are appended with defaults.

## Rendering

- Public header derives visible built-ins and custom entries from `/api/status`.
- The authenticated sidebar derives the configured order after the existing
  role and route visibility filters.
- Invoice visibility is evaluated from the status/config feature flag at the
  navigation boundary; route-level authorization remains authoritative.

## Verification

- Unit tests for parsing, migration defaults, ordering, hidden entries, and
  custom URL validation.
- Frontend typecheck, focused tests, lint/format checks, and production build.
