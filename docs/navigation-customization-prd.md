# Navigation Customization PRD

## Goal

Allow administrators to control the public header navigation and authenticated
sidebar without changing route contracts or permission checks.

## Requirements

- Header navigation keeps Home fixed and lets administrators reorder, hide, or
  add links with a title, URL, and new-tab flag.
- Header navigation settings are edited at `/system-settings/site/header-navigation`.
- Sidebar modules can be reordered and hidden from
  `/system-settings/site/sidebar-modules` while preserving role and route guards.
- The invoice navigation entry is hidden when invoice support is disabled.
- Wallet payment method tiles use their content width with a minimum width and
  no artificial maximum width.
- The Playground navigation item uses a conversation icon.

## Acceptance Criteria

1. Existing saved settings remain valid through backward-compatible defaults.
2. Invalid custom URLs cannot be saved as navigation entries.
3. Hidden or reordered entries update public and authenticated navigation after
   the system status refreshes.
4. Invoice navigation visibility follows the same server feature flag used by
   the invoice route.
5. Desktop and mobile layouts have no clipped labels or horizontal overflow.

## Out Of Scope

- Changing route authorization or removing existing routes.
- Per-user navigation preferences.
