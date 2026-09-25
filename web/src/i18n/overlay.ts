/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import enOverlay from './overlay/en.json'
import frOverlay from './overlay/fr.json'
import jaOverlay from './overlay/ja.json'
import ruOverlay from './overlay/ru.json'
import viOverlay from './overlay/vi.json'
import zhTWOverlay from './overlay/zh-TW.json'
import zhOverlay from './overlay/zh.json'

/**
 * ============================================================================
 * Fork-owned i18n overlay
 * ============================================================================
 *
 * `src/i18n/locales/*.json` are upstream-owned bundles: they must stay
 * byte-identical to the official release so an upstream sync never conflicts
 * there. Everything Tokeness adds or re-words lives in `src/i18n/overlay/`:
 *
 * - `translation` — keys upstream does not ship (fork features: invoices,
 *   partner console, business analysis, …).
 * - `overrides` — keys upstream ships but we word differently.
 *
 * Both maps are merged over the upstream bundle at startup, so call sites keep
 * using one flat key space and no component needs to know where a string came
 * from. The invariants (identical key set across the 7 locales, no collision
 * with a key upstream later adopts, no stale override) are enforced by
 * `scripts/fork-invariants/main.mjs --check i18n`; run it after touching
 * locales. See FORK-CHANGES.md §7 and §10.
 */
export type LocaleBundle = { translation: Record<string, string> }
export type LocaleOverlay = {
  translation: Record<string, string>
  overrides: Record<string, string>
}

/** Overlay keyed by locale, matching the file names under `src/i18n/locales`. */
export const FORK_LOCALE_OVERLAY: Record<string, LocaleOverlay> = {
  en: enOverlay,
  fr: frOverlay,
  ja: jaOverlay,
  ru: ruOverlay,
  vi: viOverlay,
  zh: zhOverlay,
  'zh-TW': zhTWOverlay,
}

/**
 * i18next resource keys (as used by `resources` in config.ts) mapped onto the
 * overlay/locale file names. A resource key missing here is an error rather
 * than a silent English fallback.
 */
export const RESOURCE_TO_OVERLAY: Record<string, string> = {
  en: 'en',
  zhCN: 'zh',
  zhTW: 'zh-TW',
  fr: 'fr',
  ru: 'ru',
  ja: 'ja',
  vi: 'vi',
}

/** Merge one overlay over one upstream bundle; the overlay always wins. */
export function mergeLocaleBundle(
  bundle: LocaleBundle,
  overlay: LocaleOverlay
): LocaleBundle {
  return {
    translation: {
      ...bundle.translation,
      ...overlay.translation,
      ...overlay.overrides,
    },
  }
}

/**
 * Return a copy of the i18next resources with the fork overlay applied. Call
 * this before any edition-specific rewording (the mainland currency pass) so
 * the fork's own strings are worded exactly like the rest of the bundle.
 */
export function withForkLocaleOverlay<T extends Record<string, LocaleBundle>>(
  resources: T
): T {
  const merged: Record<string, LocaleBundle> = {}

  for (const [locale, bundle] of Object.entries(resources)) {
    const overlayKey = RESOURCE_TO_OVERLAY[locale]
    if (!overlayKey) {
      throw new Error(
        `No fork i18n overlay mapping for locale "${locale}"; add it to RESOURCE_TO_OVERLAY in src/i18n/overlay.ts`
      )
    }
    const overlay = FORK_LOCALE_OVERLAY[overlayKey]
    if (!overlay) {
      throw new Error(
        `Missing fork i18n overlay file for locale "${overlayKey}"`
      )
    }
    merged[locale] = mergeLocaleBundle(bundle, overlay)
  }

  return merged as T
}
