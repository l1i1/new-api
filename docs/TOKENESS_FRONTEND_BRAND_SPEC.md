# Tokeness Frontend Brand Spec

## Design Read

- Artifact: developer infrastructure landing page inside the New API product
- Audience: developers and operators evaluating or using the Tokeness gateway
- Visual language: operational grid, sharp modules, and fixed Tokeness red
- Mode: extension-preserve
- Dials: visual variance 3, motion 2, information density 7, asset dependence 4,
  brand fidelity 10

## Preserve

- Existing public header, route structure, authentication behavior,
  system-configured name/logo, legal links, and project attribution
- Current light/dark theme variables and user-selected neutral surfaces
- Public Sans body typography and the existing type scale
- Current keyboard, focus, reduced-motion, and responsive behavior
- The legacy home module order, provider wall, system statistics, routing/spec
  blocks, Tokeness footer, and New API project attribution

## Improve

- Replace generic gateway copy with the established Tokeness value proposition
- Express the request path, quota control, routing, and audit story with native
  components rather than injected markup
- Keep the page scannable on mobile while preserving the legacy composition

## Remove

- CDN-injected page HTML, inline provider SVG markup, fetch/XHR interception,
  history patching, and DOM mutation observers
- Unsupported pricing or affiliate claims

## Design System

- Color: neutral surfaces use the existing semantic theme tokens. The home page
  retains its reviewed Tokeness red `#d7192a` accent and neutral grid lines.
- Typography: Public Sans from the existing application. Monospace is limited
  to API paths, keys, and protocol examples.
- Spacing: existing Tailwind spacing scale with a 4px base; section rhythm must
  match the current public layout.
- Radius: home modules remain 2 px or square as defined by the legacy page.
  The rest of the application continues to use runtime theme customization.
- Preset: the Tokeness default color preset is `sunset-glow`; existing user
  cookies remain authoritative.
- Shadows: existing border-first, low-elevation treatment; no decorative glow.
- Motion: existing short fade/translate utilities and state transitions. Honor
  `prefers-reduced-motion` and do not add scroll-jacking.

## Assets

- Runtime brand logo: system-configured `logo`, with `/logo.png` as the existing
  fallback.
- Provider marks: the already installed `@lobehub/icons` package.
- Product visual: the legacy system diagram, capability matrix, statistics,
  provider wall, and specification table.
- External asset: the reviewed LM Speed verification badge used by the legacy
  Tokeness footer.

## Protected Contracts

- `/`, `/dashboard`, `/pricing`, auth routes, legal routes, and configured docs
  links
- `HomePageContent` external URL, HTML, and Markdown behavior
- System settings field names and raw persisted values
- Existing analytics placeholders and upstream project identity/attribution
- Payment amount calculation and affiliate transfer behavior
