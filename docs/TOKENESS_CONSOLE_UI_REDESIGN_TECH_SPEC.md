# Tokeness Console UI Redesign Tech Spec

## Visual Tokens

| Role | Light | Dark |
| --- | --- | --- |
| Canvas | `#f7f6f2` | `#0b0f14` |
| Ink | `#151515` | `#f5f7fb` |
| Brand accent | `#d7192a` | `#ef3340` |
| Rule | `#d8d6cf` | `#29313a` |
| Success | semantic success token | semantic success token |
| Warning | semantic warning token | semantic warning token |

- Use `Public Sans` for application text and existing Lora only for editorial
  emphasis. Use system monospace for model IDs, API paths, and request data.
- Use a 4px spacing base and existing Tailwind spacing utilities.
- Prefer square or 2-4px geometry. Avoid new `rounded-2xl`, glass surfaces,
  capsule buttons, decorative shadows, or gradients in the Tokeness shell.
- Keep the red accent sparse: active rules, primary actions, and semantic
  attention states only.

## Application Shell

- Keep the current `SidebarProvider`, mobile Sheet behavior, and sidebar cookie.
- Restyle the desktop shell as a fixed 216px navigation rail with a full-width
  56px header and 1px rules.
- Keep navigation URLs and visibility filtering from `useSidebarConfig`.
- Render active navigation with a 2px Tokeness red rule and text emphasis, not a
  rounded blue background block.
- Show brand mark/name in one compact row. Move version information to system
  information/about surfaces.
- Keep search, notifications, language, currency, theme, and profile actions;
  reduce their visual weight and preserve accessible names/tooltips.

## Dashboard

- Replace decorative overview gradients with rule-based surfaces.
- Use a KPI strip for headline metrics, followed by a two-column chart/health
  composition and dense ledger tables.
- Preserve existing data queries, filters, charts, lazy loading, and admin-only
  sections. Only change framing, hierarchy, and surface styling.

## Public Surfaces

- Keep the native Tokeness home grid and reviewed section order.
- Replace floating rounded public navigation with a full-width rule-based bar.
- Present pricing/models as a specification table with input/output/cache
  prices, route availability, and last-verified context.
- Keep auth fields, form order, redirects, legal consent, and error behavior;
  only change the visual composition.

## Implementation Order

1. Add tokens and shell primitives.
2. Restyle header, sidebar, nav groups, system brand, and page layout.
3. Restyle dashboard panel wrappers and overview surfaces.
4. Restyle public header, pricing/model surfaces, and auth framing.
5. Add focused layout/interaction tests and run typecheck, lint, format, and
   production build.

## Verification

- `bun run typecheck`
- `bun run lint`
- `bun run format:check`
- `bun run build`
- Focused frontend tests for sidebar state, responsive actions, preserved routes,
  and dashboard rendering.
- Browser screenshots at 1440x900 and 390x844 in light and dark themes.
