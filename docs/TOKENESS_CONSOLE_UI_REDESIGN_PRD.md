# Tokeness Console UI Redesign PRD

## Problem

The authenticated web application is functionally mature but visually close to
the generic New API / SaaS dashboard pattern: a floating utility header,
rounded sidebar controls, large card grids, and theme presets that dilute the
Tokeness identity. TokenRouter provides a useful contrast: its public surface
uses a blue, capsule-shaped marketing language. Tokeness must establish a
different visual and trust model rather than copy either surface.

## Product Direction

Tokeness becomes an evidence-first AI gateway console. The UI should feel like
an operator's control room: quiet, dense, editorial, and explicit about model
prices, route health, usage, and billing evidence.

## Audience

- Developers creating API credentials and comparing model prices.
- Operators checking route health, usage, and billing records.
- Administrators managing channels, models, users, and system settings.

## Scope

### In scope

- Authenticated application shell and navigation.
- Theme tokens and the default Tokeness visual preset.
- Dashboard overview and model analytics framing.
- Public header, pricing/model presentation, and sign-in framing.
- Responsive desktop/mobile behavior and visual regression coverage.

### Out of scope

- Route paths, API contracts, permission rules, billing calculations, or
  persisted user data.
- Removal of New API / QuantumNous project attribution or legal metadata.
- Replacement of the existing native Tokeness home information architecture.

## Success Criteria

1. A first-time user can identify Tokeness, API keys, model prices, route
   status, and wallet access within 10 seconds.
2. The authenticated shell no longer reads as a default New API installation.
3. Pricing exposes input, output, and cache values with verification context.
4. Existing routes, permissions, i18n, keyboard behavior, reduced motion, and
   mobile navigation remain functional.
5. Desktop (1440x900) and mobile (390x844) screenshots have no overlap,
   horizontal overflow, clipped labels, or unreachable primary actions.

## Design Read

- Artifact: authenticated control room plus public pricing/auth surfaces.
- Visual language: institutional data console with Tokeness editorial red.
- Mode: redesign-preserve.
- Dials: visual variance 6, motion 3, information density 7, asset dependence
  3, brand fidelity 8.

## Protected Contracts

- Existing routes, slugs, deep links, form names, API behavior, and analytics
  selectors.
- System-configured logo/name and native Tokeness home behavior.
- i18n coverage, accessibility semantics, focus order, and reduced-motion
  behavior.
- New API / QuantumNous attribution, legal links, and project metadata.
