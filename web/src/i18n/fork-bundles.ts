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
import enJson from './locales/en.json'
import frJson from './locales/fr.json'
import jaJson from './locales/ja.json'
import ruJson from './locales/ru.json'
import viJson from './locales/vi.json'
import zhTWJson from './locales/zh-TW.json'
import zhJson from './locales/zh.json'
import { FORK_LOCALE_OVERLAY, mergeLocaleBundle } from './overlay'

/**
 * ============================================================================
 * The bundles the app actually speaks
 * ============================================================================
 *
 * `src/i18n/locales/*.json` are upstream-owned and carry nothing Tokeness
 * added; the fork delta lives in `src/i18n/overlay/`. Anything that needs a
 * *complete* bundle — the runtime config and every test that renders UI text —
 * imports from here instead of reaching into `locales/` directly, because the
 * upstream bundle alone silently loses fork strings and the UI falls back to
 * raw keys.
 *
 * Composed exactly like the runtime: overlay first, then the edition-specific
 * rewording (mainland currency) applied by `config.ts`.
 */
export const en = mergeLocaleBundle(enJson, FORK_LOCALE_OVERLAY.en)
export const zhCN = mergeLocaleBundle(zhJson, FORK_LOCALE_OVERLAY.zh)
export const zhTW = mergeLocaleBundle(zhTWJson, FORK_LOCALE_OVERLAY['zh-TW'])
export const fr = mergeLocaleBundle(frJson, FORK_LOCALE_OVERLAY.fr)
export const ru = mergeLocaleBundle(ruJson, FORK_LOCALE_OVERLAY.ru)
export const ja = mergeLocaleBundle(jaJson, FORK_LOCALE_OVERLAY.ja)
export const vi = mergeLocaleBundle(viJson, FORK_LOCALE_OVERLAY.vi)

/** The bundles keyed by i18next resource key (what `config.ts` registers). */
export const FORK_LOCALE_BUNDLES = { en, zhCN, fr, ru, ja, vi, zhTW } as const

/** The same bundles keyed by locale file name, for file-oriented call sites. */
export const FORK_BUNDLES_BY_FILE = {
  en,
  zh: zhCN,
  'zh-TW': zhTW,
  fr,
  ru,
  ja,
  vi,
} as const
