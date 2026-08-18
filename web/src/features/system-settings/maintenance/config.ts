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
import { isSafeHeaderNavUrl, type HeaderNavItem } from '@/lib/nav-modules'

export { isSafeHeaderNavUrl }
export type { HeaderNavItem }

export type HeaderNavAccessConfig = {
  enabled: boolean
  requireAuth: boolean
}

export type HeaderNavModulesConfig = {
  home: boolean
  console: boolean
  pricing: HeaderNavAccessConfig
  rankings: HeaderNavAccessConfig
  docs: boolean
  about: boolean
  items: HeaderNavItem[]
  [key: string]: boolean | HeaderNavAccessConfig | HeaderNavItem[]
}

export type SidebarSectionConfig = {
  enabled: boolean
  [key: string]: boolean
}

export type SidebarModulesAdminConfig = Record<string, SidebarSectionConfig>

export const HEADER_NAV_DEFAULT: HeaderNavModulesConfig = {
  home: true,
  console: true,
  pricing: {
    enabled: true,
    requireAuth: false,
  },
  rankings: {
    enabled: true,
    requireAuth: false,
  },
  docs: true,
  about: true,
  items: [
    {
      id: 'console',
      title: 'Console',
      url: '/dashboard',
      newTab: false,
      visible: true,
    },
    {
      id: 'pricing',
      title: 'Model Square',
      url: '/pricing',
      newTab: false,
      visible: true,
    },
    {
      id: 'rankings',
      title: 'Rankings',
      url: '/rankings',
      newTab: false,
      visible: true,
    },
    {
      id: 'docs',
      title: 'Docs',
      url: '/docs',
      newTab: false,
      visible: true,
    },
    {
      id: 'about',
      title: 'About',
      url: '/about',
      newTab: false,
      visible: true,
    },
  ],
}

export const SIDEBAR_MODULES_DEFAULT: SidebarModulesAdminConfig = {
  chat: {
    enabled: true,
    playground: true,
    chat: true,
  },
  console: {
    enabled: true,
    detail: true,
    token: true,
    log: true,
    midjourney: true,
    task: true,
  },
  personal: {
    enabled: true,
    topup: true,
    personal: true,
    invoice: true,
  },
  admin: {
    enabled: true,
    channel: true,
    models: true,
    redemption: true,
    user: true,
    setting: true,
    subscription: true,
    invoice_admin: true,
    system_info: true,
  },
}

const toBoolean = (value: unknown, fallback: boolean): boolean => {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value === 1
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase()
    if (normalized === 'true' || normalized === '1') return true
    if (normalized === 'false' || normalized === '0') return false
  }
  return fallback
}

const cloneHeaderNavDefault = (): HeaderNavModulesConfig => ({
  ...HEADER_NAV_DEFAULT,
  pricing: { ...HEADER_NAV_DEFAULT.pricing },
  rankings: { ...HEADER_NAV_DEFAULT.rankings },
  items: HEADER_NAV_DEFAULT.items.map((item) => ({ ...item })),
})

const parseHeaderNavItem = (
  raw: unknown,
  index: number
): HeaderNavItem | null => {
  if (!raw || typeof raw !== 'object') return null
  const record = raw as Record<string, unknown>
  const rawId = typeof record.id === 'string' ? record.id.trim() : ''
  const rawTitle = typeof record.title === 'string' ? record.title.trim() : ''
  const rawUrl = typeof record.url === 'string' ? record.url.trim() : ''
  const id = rawId || `custom-${index + 1}`
  if (id === 'home' || !rawTitle || !isSafeHeaderNavUrl(rawUrl)) return null

  return {
    id,
    title: rawTitle,
    url: rawUrl,
    newTab: toBoolean(record.newTab, false),
    visible: toBoolean(record.visible, true),
  }
}

const mergeHeaderNavItems = (
  rawItems: unknown,
  fallback: HeaderNavItem[],
  access: Pick<
    HeaderNavModulesConfig,
    'console' | 'pricing' | 'rankings' | 'docs' | 'about'
  >
): HeaderNavItem[] => {
  const fallbackById = new Map(fallback.map((item) => [item.id, item]))
  const visibility: Record<string, boolean> = {
    console: access.console,
    pricing: access.pricing.enabled,
    rankings: access.rankings.enabled,
    docs: access.docs,
    about: access.about,
  }
  const items: HeaderNavItem[] = []
  const seen = new Set<string>()

  if (Array.isArray(rawItems)) {
    rawItems.forEach((raw, index) => {
      const item = parseHeaderNavItem(raw, index)
      if (!item || seen.has(item.id)) return
      seen.add(item.id)
      const builtin = fallbackById.get(item.id)
      if (builtin) {
        items.push({ ...builtin, visible: item.visible })
        return
      }
      items.push(item)
    })
  }

  fallback.forEach((item) => {
    if (seen.has(item.id)) return
    seen.add(item.id)
    items.push({ ...item, visible: visibility[item.id] ?? item.visible })
  })

  return items
}

const parseAccessModule = (
  raw: unknown,
  fallback: HeaderNavAccessConfig
): HeaderNavAccessConfig => {
  if (
    typeof raw === 'boolean' ||
    typeof raw === 'string' ||
    typeof raw === 'number'
  ) {
    return {
      enabled: toBoolean(raw, fallback.enabled),
      requireAuth: fallback.requireAuth,
    }
  }
  if (raw && typeof raw === 'object') {
    const record = raw as Record<string, unknown>
    return {
      enabled: toBoolean(record.enabled, fallback.enabled),
      requireAuth: toBoolean(record.requireAuth, fallback.requireAuth),
    }
  }
  return { ...fallback }
}

const cloneSidebarDefault = (): SidebarModulesAdminConfig =>
  Object.entries(SIDEBAR_MODULES_DEFAULT).reduce<SidebarModulesAdminConfig>(
    (acc, [section, config]) => {
      acc[section] = { ...config }
      return acc
    },
    {}
  )

export function parseHeaderNavModules(
  value: string | null | undefined
): HeaderNavModulesConfig {
  const base = cloneHeaderNavDefault()
  if (!value) {
    return base
  }
  try {
    const parsed = JSON.parse(value) as Record<string, unknown>
    const result: HeaderNavModulesConfig = {
      ...base,
      pricing: { ...base.pricing },
      rankings: { ...base.rankings },
      items: base.items.map((item) => ({ ...item })),
    }

    Object.entries(parsed).forEach(([key, raw]) => {
      if (key === 'pricing') {
        result.pricing = parseAccessModule(raw, base.pricing)
        return
      }
      if (key === 'rankings') {
        result.rankings = parseAccessModule(raw, base.rankings)
        return
      }
      if (key === 'items') {
        return
      }

      if (typeof raw === 'boolean') {
        result[key] = raw
        return
      }
      if (typeof raw === 'string' || typeof raw === 'number') {
        result[key] = toBoolean(raw, Boolean(base[key]))
        return
      }
    })

    result.items = mergeHeaderNavItems(parsed.items, base.items, {
      console: result.console,
      pricing: result.pricing,
      rankings: result.rankings,
      docs: result.docs,
      about: result.about,
    })

    // Keep the legacy access fields authoritative for backend route guards
    // while allowing the ordered item list to represent visibility.
    const itemVisibility = new Map(
      result.items.map((item) => [item.id, item.visible])
    )
    result.console = itemVisibility.get('console') ?? result.console
    result.docs = itemVisibility.get('docs') ?? result.docs
    result.about = itemVisibility.get('about') ?? result.about
    result.pricing.enabled =
      itemVisibility.get('pricing') ?? result.pricing.enabled
    result.rankings.enabled =
      itemVisibility.get('rankings') ?? result.rankings.enabled
    result.home = true

    return result
  } catch {
    return base
  }
}

export function serializeHeaderNavModules(
  config: HeaderNavModulesConfig
): string {
  return JSON.stringify(config)
}

/** Move one public navigation entry while preserving all other entries. */
export function moveHeaderNavItem(
  items: HeaderNavItem[],
  itemId: string,
  offset: -1 | 1
): HeaderNavItem[] {
  const index = items.findIndex((item) => item.id === itemId)
  const nextIndex = index + offset
  if (index < 0 || nextIndex < 0 || nextIndex >= items.length) return items

  const next = [...items]
  const [moved] = next.splice(index, 1)
  next.splice(nextIndex, 0, moved)
  return next
}

export function parseSidebarModulesAdmin(
  value: string | null | undefined
): SidebarModulesAdminConfig {
  const defaults = cloneSidebarDefault()
  // If empty string, null, or undefined, use default config
  if (!value || value.trim() === '') return defaults

  try {
    const parsed = JSON.parse(value) as Record<string, unknown>
    const result: SidebarModulesAdminConfig = {}

    Object.entries(parsed).forEach(([sectionKey, raw]) => {
      if (!raw || typeof raw !== 'object') return

      const defaultSection = defaults[sectionKey] ?? { enabled: true }
      const sectionConfig: SidebarSectionConfig = {
        enabled: toBoolean(
          (raw as Record<string, unknown>).enabled,
          defaultSection.enabled ?? true
        ),
      }

      Object.entries(raw as Record<string, unknown>).forEach(
        ([moduleKey, moduleValue]) => {
          if (moduleKey === 'enabled') return
          sectionConfig[moduleKey] = toBoolean(
            moduleValue,
            defaultSection[moduleKey] ?? true
          )
        }
      )

      result[sectionKey] = sectionConfig
    })

    // Merge defaults to ensure expected sections exist
    Object.entries(defaults).forEach(([sectionKey, config]) => {
      if (!result[sectionKey]) {
        result[sectionKey] = { ...config }
        return
      }

      Object.entries(config).forEach(([moduleKey, moduleValue]) => {
        if (!(moduleKey in result[sectionKey])) {
          result[sectionKey][moduleKey] = moduleValue
        }
      })
    })

    return result
  } catch {
    return defaults
  }
}

export function serializeSidebarModulesAdmin(
  config: SidebarModulesAdminConfig
): string {
  return JSON.stringify(config)
}

/** Move one sidebar entry while preserving the order of all other entries. */
export function moveSidebarItem(
  items: string[],
  item: string,
  offset: -1 | 1
): string[] {
  const index = items.indexOf(item)
  const nextIndex = index + offset
  if (index < 0 || nextIndex < 0 || nextIndex >= items.length) return items

  const next = [...items]
  const [moved] = next.splice(index, 1)
  next.splice(nextIndex, 0, moved)
  return next
}
