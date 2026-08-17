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
import { getStatus } from '@/lib/api'

export type ModuleAccess = { enabled: boolean; requireAuth: boolean }

export type HeaderNavModule = 'rankings' | 'pricing'

export type HeaderNavItem = {
  id: string
  title: string
  url: string
  newTab: boolean
  visible: boolean
}

export type HeaderNavModules = {
  home: boolean
  console: boolean
  pricing: ModuleAccess
  rankings: ModuleAccess
  docs: boolean
  about: boolean
  items: HeaderNavItem[]
  [key: string]: boolean | ModuleAccess | HeaderNavItem[]
}

const DEFAULT_HEADER_NAV_MODULES: HeaderNavModules = {
  home: true,
  console: true,
  pricing: { enabled: true, requireAuth: false },
  rankings: { enabled: true, requireAuth: false },
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

const DEFAULTS: Record<HeaderNavModule, ModuleAccess> = {
  pricing: DEFAULT_HEADER_NAV_MODULES.pricing,
  rankings: DEFAULT_HEADER_NAV_MODULES.rankings,
}

function cloneHeaderNavDefaults(): HeaderNavModules {
  return {
    ...DEFAULT_HEADER_NAV_MODULES,
    pricing: { ...DEFAULT_HEADER_NAV_MODULES.pricing },
    rankings: { ...DEFAULT_HEADER_NAV_MODULES.rankings },
    items: DEFAULT_HEADER_NAV_MODULES.items.map((item) => ({ ...item })),
  }
}

export function parseHeaderNavBoolean(
  raw: unknown,
  fallback: boolean
): boolean {
  if (typeof raw === 'boolean') return raw
  if (typeof raw === 'number') {
    if (raw === 1) return true
    if (raw === 0) return false
    return fallback
  }
  if (typeof raw === 'string') {
    const normalized = raw.trim().toLowerCase()
    if (normalized === 'true' || normalized === '1') return true
    if (normalized === 'false' || normalized === '0') return false
  }
  return fallback
}

function parseAccess(raw: unknown, fallback: ModuleAccess): ModuleAccess {
  if (
    typeof raw === 'boolean' ||
    typeof raw === 'number' ||
    typeof raw === 'string'
  ) {
    return {
      enabled: parseHeaderNavBoolean(raw, fallback.enabled),
      requireAuth: fallback.requireAuth,
    }
  }
  if (raw && typeof raw === 'object') {
    const r = raw as Record<string, unknown>
    return {
      enabled: parseHeaderNavBoolean(r.enabled, fallback.enabled),
      requireAuth: parseHeaderNavBoolean(r.requireAuth, fallback.requireAuth),
    }
  }
  return { ...fallback }
}

export function isSafeHeaderNavUrl(value: string): boolean {
  const url = value.trim()
  if (!url) return false
  if (
    [...url].some(
      (char) => char <= '\u001f' || char === '\u007f' || char === '\\'
    )
  ) {
    return false
  }
  if (/^\/(?!\/)/.test(url)) return true
  try {
    const parsed = new URL(url)
    return (
      (parsed.protocol === 'http:' || parsed.protocol === 'https:') &&
      Boolean(parsed.hostname)
    )
  } catch {
    return false
  }
}

function parseHeaderNavItem(raw: unknown, index: number): HeaderNavItem | null {
  if (!raw || typeof raw !== 'object') return null
  const record = raw as Record<string, unknown>
  const id = typeof record.id === 'string' ? record.id.trim() : ''
  const title = typeof record.title === 'string' ? record.title.trim() : ''
  const url = typeof record.url === 'string' ? record.url.trim() : ''
  if (id === 'home' || !title || !isSafeHeaderNavUrl(url)) return null

  return {
    id: id || `custom-${index + 1}`,
    title,
    url,
    newTab: parseHeaderNavBoolean(record.newTab, false),
    visible: parseHeaderNavBoolean(record.visible, true),
  }
}

function mergeHeaderNavItems(
  rawItems: unknown,
  fallback: HeaderNavItem[],
  modules: Pick<
    HeaderNavModules,
    'console' | 'pricing' | 'rankings' | 'docs' | 'about'
  >
): HeaderNavItem[] {
  const fallbackById = new Map(fallback.map((item) => [item.id, item]))
  const legacyVisibility: Record<string, boolean> = {
    console: modules.console,
    pricing: modules.pricing.enabled,
    rankings: modules.rankings.enabled,
    docs: modules.docs,
    about: modules.about,
  }
  const items: HeaderNavItem[] = []
  const seen = new Set<string>()

  if (Array.isArray(rawItems)) {
    rawItems.forEach((raw, index) => {
      const item = parseHeaderNavItem(raw, index)
      if (!item || seen.has(item.id)) return
      seen.add(item.id)
      const builtin = fallbackById.get(item.id)
      items.push(builtin ? { ...builtin, visible: item.visible } : item)
    })
  }

  fallback.forEach((item) => {
    if (seen.has(item.id)) return
    seen.add(item.id)
    items.push({ ...item, visible: legacyVisibility[item.id] ?? item.visible })
  })

  return items
}

function parseHeaderNavRecord(raw: unknown): Record<string, unknown> | null {
  if (!raw || String(raw).trim() === '') return null
  if (raw && typeof raw === 'object') return raw as Record<string, unknown>

  try {
    return JSON.parse(String(raw)) as Record<string, unknown>
  } catch {
    return null
  }
}

export function parseHeaderNavModules(raw: unknown): HeaderNavModules {
  const result = cloneHeaderNavDefaults()
  const parsed = parseHeaderNavRecord(raw)
  if (!parsed) return result

  Object.entries(parsed).forEach(([key, value]) => {
    if (key === 'pricing') {
      result.pricing = parseAccess(value, result.pricing)
      return
    }
    if (key === 'rankings') {
      result.rankings = parseAccess(value, result.rankings)
      return
    }
    if (key === 'items') return

    const fallback = result[key]
    if (
      typeof fallback === 'boolean' ||
      typeof value === 'boolean' ||
      typeof value === 'number' ||
      typeof value === 'string'
    ) {
      result[key] = parseHeaderNavBoolean(
        value,
        typeof fallback === 'boolean' ? fallback : true
      )
    }
  })

  const { items: _items, ...moduleAccess } = result
  result.items = mergeHeaderNavItems(parsed.items, result.items, moduleAccess)

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
}

export function parseHeaderNavModulesFromStatus(
  status: Record<string, unknown> | null
): HeaderNavModules {
  return parseHeaderNavModules(status?.HeaderNavModules)
}

function getCachedStatus(): Record<string, unknown> | null {
  try {
    if (typeof window === 'undefined') return null
    const raw = window.localStorage.getItem('status')
    return raw ? (JSON.parse(raw) as Record<string, unknown>) : null
  } catch {
    return null
  }
}

function cacheStatus(status: Record<string, unknown> | null): void {
  try {
    if (typeof window !== 'undefined' && status) {
      window.localStorage.setItem('status', JSON.stringify(status))
    }
  } catch {
    /* empty */
  }
}

export function getModuleAccessFromStatus(
  status: Record<string, unknown> | null,
  module: HeaderNavModule
): ModuleAccess {
  return parseHeaderNavModulesFromStatus(status)[module] ?? DEFAULTS[module]
}

export function getModuleAccess(module: HeaderNavModule): ModuleAccess {
  return getModuleAccessFromStatus(getCachedStatus(), module)
}

export async function getFreshModuleAccess(
  module: HeaderNavModule
): Promise<ModuleAccess> {
  try {
    const status = (await getStatus()) as Record<string, unknown> | null
    cacheStatus(status)
    return getModuleAccessFromStatus(status, module)
  } catch {
    return { enabled: false, requireAuth: true }
  }
}

export function isSidebarModuleEnabled(
  section: string,
  module: string
): boolean {
  const status = getCachedStatus()
  if (!status) return true

  const raw = status.SidebarModulesAdmin
  if (!raw || String(raw).trim() === '') return true

  try {
    const parsed = JSON.parse(String(raw)) as Record<
      string,
      Record<string, boolean>
    >
    const sectionConfig = parsed[section]
    if (!sectionConfig) return true
    if (sectionConfig.enabled === false) return false
    if (sectionConfig[module] === false) return false
    return true
  } catch {
    return true
  }
}
