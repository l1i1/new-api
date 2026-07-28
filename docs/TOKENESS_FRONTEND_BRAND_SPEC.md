# Tokeness Frontend Brand Spec

## Design Read

- Artifact: developer infrastructure landing page inside the New API product
- Audience: developers and operators evaluating or using the Tokeness gateway
- Visual language: restrained builder SaaS with operational clarity
- Mode: redesign-preserve
- Dials: visual variance 4, motion 3, information density 5, asset dependence 3,
  brand fidelity 9

## Preserve

- Existing public header, footer, route structure, authentication behavior,
  system-configured name/logo, legal links, and project attribution
- Current light/dark theme variables and user-selected theme customization
- Public Sans body typography and the existing type scale
- Current keyboard, focus, reduced-motion, and responsive behavior

## Improve

- Replace generic gateway copy with the established Tokeness value proposition
- Express the request path, quota control, routing, and audit story with native
  components rather than injected markup
- Keep the page scannable on mobile and avoid oversized marketing typography

## Remove

- Global `border-radius: 0` overrides
- CDN-injected page HTML, inline provider SVG walls, fetch/XHR interception,
  history patching, and DOM mutation observers
- Unsupported pricing or affiliate claims

## Design System

- Color: use `background`, `foreground`, `muted`, `border`, `primary`, and other
  existing semantic theme tokens. Do not introduce a page-only palette.
- Typography: Public Sans from the existing application. Monospace is limited
  to API paths, keys, and protocol examples.
- Spacing: existing Tailwind spacing scale with a 4px base; section rhythm must
  match the current public layout.
- Radius: use the runtime `--radius` system. The Tokeness default is the
  explicit square `none` setting, while user customization remains authoritative.
- Preset: the Tokeness default color preset is `sunset-glow`; existing user
  cookies remain authoritative.
- Shadows: existing border-first, low-elevation treatment; no decorative glow.
- Motion: existing short fade/translate utilities and state transitions. Honor
  `prefers-reduced-motion` and do not add scroll-jacking.

## Assets

- Runtime brand logo: system-configured `logo`, with `/logo.png` as the existing
  fallback.
- Provider marks: the already installed `@lobehub/icons` package.
- Product visual: the existing native terminal/API demonstration component.
- No external hotlinked brand assets are required for this migration.

## Protected Contracts

- `/`, `/dashboard`, `/pricing`, auth routes, legal routes, and configured docs
  links
- `HomePageContent` external URL, HTML, and Markdown behavior
- System settings field names and raw persisted values
- Existing analytics placeholders and upstream project identity/attribution
- Payment amount calculation and affiliate transfer behavior
