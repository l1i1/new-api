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
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react'

import {
  type ContentLayout,
  DEFAULT_THEME_CUSTOMIZATION,
  type ThemeCustomization,
  type ThemeFont,
  type ThemePreset,
  type ThemeRadius,
  type ThemeScale,
} from '@/lib/theme-customization'
import {
  applyThemeCustomizationToDom,
  readThemeCustomization,
} from '@/lib/theme-customization-storage'
import {
  THEME_STORAGE_KEYS,
  writeThemePreference,
} from '@/lib/theme-storage'

type ThemeCustomizationContextType = {
  defaults: ThemeCustomization
  customization: ThemeCustomization
  setPreset: (preset: ThemePreset) => void
  setFont: (font: ThemeFont) => void
  setRadius: (radius: ThemeRadius) => void
  setScale: (scale: ThemeScale) => void
  setContentLayout: (contentLayout: ContentLayout) => void
  resetCustomization: () => void
}

// Fallback used when a consumer renders outside the provider (e.g. an error
// route mounted before providers are ready, or stale HMR boundaries). Keeping
// it permissive prevents the whole tree from crashing — the UI just behaves
// like the defaults until the real provider re-mounts.
const FALLBACK_CONTEXT: ThemeCustomizationContextType = {
  defaults: DEFAULT_THEME_CUSTOMIZATION,
  customization: DEFAULT_THEME_CUSTOMIZATION,
  setPreset: () => {},
  setFont: () => {},
  setRadius: () => {},
  setScale: () => {},
  setContentLayout: () => {},
  resetCustomization: () => {},
}

const ThemeCustomizationContext =
  createContext<ThemeCustomizationContextType>(FALLBACK_CONTEXT)

export function ThemeCustomizationProvider(props: {
  children: React.ReactNode
}) {
  // One aggregate read keeps the provider and the pre-mount DOM bootstrap
  // (initializeThemeCustomizationDom) on the same source of truth.
  const initialCustomization = useMemo(readThemeCustomization, [])
  const [preset, _setPreset] = useState<ThemePreset>(
    initialCustomization.preset
  )
  const [font, _setFont] = useState<ThemeFont>(initialCustomization.font)
  const [radius, _setRadius] = useState<ThemeRadius>(
    initialCustomization.radius
  )
  const [scale, _setScale] = useState<ThemeScale>(initialCustomization.scale)
  const [contentLayout, _setContentLayout] = useState<ContentLayout>(
    initialCustomization.contentLayout
  )

  useEffect(() => {
    applyThemeCustomizationToDom({
      preset,
      font,
      radius,
      scale,
      contentLayout,
    })
  }, [contentLayout, font, preset, radius, scale])

  const setPreset = useCallback((value: ThemePreset) => {
    _setPreset(value)
    writeThemePreference(
      THEME_STORAGE_KEYS.preset,
      value === DEFAULT_THEME_CUSTOMIZATION.preset ? null : value
    )
  }, [])

  const setFont = useCallback((value: ThemeFont) => {
    _setFont(value)
    writeThemePreference(
      THEME_STORAGE_KEYS.font,
      value === DEFAULT_THEME_CUSTOMIZATION.font ? null : value
    )
  }, [])

  const setRadius = useCallback((value: ThemeRadius) => {
    _setRadius(value)
    writeThemePreference(
      THEME_STORAGE_KEYS.radius,
      value === DEFAULT_THEME_CUSTOMIZATION.radius ? null : value
    )
  }, [])

  const setScale = useCallback((value: ThemeScale) => {
    _setScale(value)
    writeThemePreference(
      THEME_STORAGE_KEYS.scale,
      value === DEFAULT_THEME_CUSTOMIZATION.scale ? null : value
    )
  }, [])

  const setContentLayout = useCallback((value: ContentLayout) => {
    _setContentLayout(value)
    writeThemePreference(
      THEME_STORAGE_KEYS.contentLayout,
      value === DEFAULT_THEME_CUSTOMIZATION.contentLayout ? null : value
    )
  }, [])

  const resetCustomization = useCallback(() => {
    setPreset(DEFAULT_THEME_CUSTOMIZATION.preset)
    setFont(DEFAULT_THEME_CUSTOMIZATION.font)
    setRadius(DEFAULT_THEME_CUSTOMIZATION.radius)
    setScale(DEFAULT_THEME_CUSTOMIZATION.scale)
    setContentLayout(DEFAULT_THEME_CUSTOMIZATION.contentLayout)
  }, [setPreset, setFont, setRadius, setScale, setContentLayout])

  const value = useMemo<ThemeCustomizationContextType>(
    () => ({
      defaults: DEFAULT_THEME_CUSTOMIZATION,
      customization: { preset, font, radius, scale, contentLayout },
      setPreset,
      setFont,
      setRadius,
      setScale,
      setContentLayout,
      resetCustomization,
    }),
    [
      preset,
      font,
      radius,
      scale,
      contentLayout,
      setPreset,
      setFont,
      setRadius,
      setScale,
      setContentLayout,
      resetCustomization,
    ]
  )

  return (
    <ThemeCustomizationContext.Provider value={value}>
      {props.children}
    </ThemeCustomizationContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useThemeCustomization() {
  return useContext(ThemeCustomizationContext)
}
