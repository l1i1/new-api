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
import { getCookie } from '@/lib/cookies'
import {
  CONTENT_LAYOUT_VALUES,
  DEFAULT_THEME_CUSTOMIZATION,
  resolveThemeFont,
  THEME_COOKIE_KEYS,
  THEME_FONT_VALUES,
  THEME_PRESET_VALUES,
  THEME_RADIUS_VALUES,
  THEME_SCALE_VALUES,
  type ThemeCustomization,
} from '@/lib/theme-customization'

function readPreference<T extends string>(
  name: string,
  allowed: ReadonlySet<T>,
  fallback: T
): T {
  const value = getCookie(name)
  return value && allowed.has(value as T) ? (value as T) : fallback
}

export function readThemeCustomization(): ThemeCustomization {
  return {
    preset: readPreference(
      THEME_COOKIE_KEYS.preset,
      THEME_PRESET_VALUES,
      DEFAULT_THEME_CUSTOMIZATION.preset
    ),
    font: readPreference(
      THEME_COOKIE_KEYS.font,
      THEME_FONT_VALUES,
      DEFAULT_THEME_CUSTOMIZATION.font
    ),
    radius: readPreference(
      THEME_COOKIE_KEYS.radius,
      THEME_RADIUS_VALUES,
      DEFAULT_THEME_CUSTOMIZATION.radius
    ),
    scale: readPreference(
      THEME_COOKIE_KEYS.scale,
      THEME_SCALE_VALUES,
      DEFAULT_THEME_CUSTOMIZATION.scale
    ),
    contentLayout: readPreference(
      THEME_COOKIE_KEYS.contentLayout,
      CONTENT_LAYOUT_VALUES,
      DEFAULT_THEME_CUSTOMIZATION.contentLayout
    ),
  }
}

function applyAttribute(name: string, value: string | null) {
  if (typeof document === 'undefined' || !document.body) return
  if (value === null) {
    document.body.removeAttribute(name)
  } else {
    document.body.setAttribute(name, value)
  }
}

export function applyThemeCustomizationToDom(
  customization: ThemeCustomization
) {
  applyAttribute(
    'data-theme-preset',
    customization.preset === 'default' ? null : customization.preset
  )
  applyAttribute(
    'data-theme-font',
    resolveThemeFont(customization.font, customization.preset)
  )
  applyAttribute(
    'data-theme-radius',
    customization.radius === 'default' ? null : customization.radius
  )
  applyAttribute(
    'data-theme-scale',
    customization.scale === 'default' ? null : customization.scale
  )
  applyAttribute('data-theme-content-layout', customization.contentLayout)
}

export function initializeThemeCustomizationDom() {
  const customization = readThemeCustomization()
  applyThemeCustomizationToDom(customization)
  return customization
}
