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
import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

import { IS_MAINLAND_SITE } from '@/lib/site-flavor'

import { convertDetectedLanguage, toDocumentLanguage } from './languages'
import en from './locales/en.json'
import fr from './locales/fr.json'
import ja from './locales/ja.json'
import ru from './locales/ru.json'
import vi from './locales/vi.json'
import zhTW from './locales/zh-TW.json'
import zhCN from './locales/zh.json'
import { withMainlandCurrencyWording } from './mainland-currency'

const baseResources = {
  en,
  zhCN,
  fr,
  ru,
  ja,
  vi,
  zhTW,
} as const

// Only the mainland edition renames the platform's own unit from USD to CNY;
// the overseas bundle keeps the upstream wording.
export const resources = IS_MAINLAND_SITE
  ? withMainlandCurrencyWording(baseResources)
  : baseResources

// The mainland edition never runs the browser-language detector; the language
// is fixed to Simplified Chinese below.
if (!IS_MAINLAND_SITE) i18n.use(LanguageDetector)
i18n.use(initReactI18next)

i18n.init({
  resources,
  // The mainland edition is Simplified Chinese only: no detection, no saved
  // preference, and changeLanguage calls for another locale are rejected.
  lng: IS_MAINLAND_SITE ? 'zhCN' : undefined,
  fallbackLng: IS_MAINLAND_SITE ? 'zhCN' : 'en',
  supportedLngs: IS_MAINLAND_SITE
    ? ['zhCN']
    : ['en', 'zhCN', 'fr', 'ru', 'ja', 'vi', 'zhTW'],
  load: 'currentOnly',
  nsSeparator: false, // Allow literal colons in keys (e.g., URLs, labels)
  debug: import.meta.env.DEV,
  interpolation: {
    escapeValue: false, // not needed for react as it escapes by default
  },
  detection: {
    // Mainland defaults to Simplified Chinese instead of the browser locale;
    // an explicit choice previously saved to localStorage still wins.
    order: IS_MAINLAND_SITE ? ['localStorage'] : ['localStorage', 'navigator'],
    caches: ['localStorage'],
    // Browsers report `zh-CN`/`zh-TW`/`zh`; map them onto our `zhCN`/`zhTW`
    // codes (non-Chinese codes pass through for normal supportedLngs matching).
    convertDetectedLanguage,
  },
})

const syncDocumentLanguage = (language?: string) => {
  document.documentElement.lang = toDocumentLanguage(language)
}

i18n.on('languageChanged', syncDocumentLanguage)
syncDocumentLanguage(i18n.resolvedLanguage || i18n.language)

export default i18n
