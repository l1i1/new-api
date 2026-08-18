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
import { useMemo } from 'react'

import type { NavGroup, NavItem } from '@/components/layout/types'
import { useStatus } from '@/hooks/use-status'
import { useAuthStore } from '@/stores/auth-store'

type SidebarSectionConfig = {
  enabled: boolean
  [key: string]: boolean
}

type SidebarModulesAdminConfig = Record<string, SidebarSectionConfig>

// User-layer config is shape-identical to admin, but may be null
// to signal "no narrowing" (empty/invalid/legacy users).
type SidebarModulesUserConfig = SidebarModulesAdminConfig | null

const INVOICE_NAVIGATION_URLS = new Set(['/invoice', '/invoices'])

/**
 * Read the invoice feature switch from the status payload. The fallback is
 * deliberately disabled: invoice navigation must not expose a feature whose
 * backend switch is absent or cannot be parsed.
 */
export function isInvoiceFeatureEnabled(
  status: Record<string, unknown> | null | undefined
): boolean {
  const raw = status?.invoice_enabled ?? status?.InvoiceEnabled
  if (typeof raw === 'boolean') return raw
  if (typeof raw === 'string') return raw.trim().toLowerCase() === 'true'
  if (typeof raw === 'number') return raw === 1
  return false
}

/**
 * Default sidebar modules configuration
 */
const DEFAULT_SIDEBAR_MODULES: SidebarModulesAdminConfig = {
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

const mergeWithDefaultSidebarModules = (
  config: SidebarModulesAdminConfig
): SidebarModulesAdminConfig => {
  const merged: SidebarModulesAdminConfig = { ...config }

  Object.entries(DEFAULT_SIDEBAR_MODULES).forEach(
    ([sectionKey, defaultSection]) => {
      const existingSection = merged[sectionKey]
      if (!existingSection) {
        merged[sectionKey] = { ...defaultSection }
        return
      }

      // Preserve the administrator's JSON key order. The settings page uses
      // that order for both the module editor and the rendered sidebar.
      const mergedSection: SidebarSectionConfig = {
        enabled: existingSection.enabled,
      }
      Object.entries(existingSection).forEach(([moduleKey, enabled]) => {
        if (moduleKey === 'enabled') return
        mergedSection[moduleKey] = enabled
      })
      Object.entries(defaultSection).forEach(([moduleKey, enabled]) => {
        if (mergedSection[moduleKey] === undefined) {
          mergedSection[moduleKey] = enabled
        }
      })
      merged[sectionKey] = mergedSection
    }
  )

  return merged
}

/**
 * Mapping from URL to configuration keys
 */
const URL_TO_CONFIG_MAP: Record<string, { section: string; module: string }> = {
  '/playground': { section: 'chat', module: 'playground' },
  '/dashboard': { section: 'console', module: 'detail' },
  '/dashboard/overview': { section: 'console', module: 'detail' },
  '/dashboard/models': { section: 'console', module: 'detail' },
  '/dashboard/users': { section: 'console', module: 'detail' },
  '/keys': { section: 'console', module: 'token' },
  '/usage-logs': { section: 'console', module: 'log' },
  '/usage-logs/common': { section: 'console', module: 'log' },
  '/usage-logs/drawing': { section: 'console', module: 'midjourney' },
  '/usage-logs/task': { section: 'console', module: 'task' },
  '/wallet': { section: 'personal', module: 'topup' },
  '/invoice': { section: 'personal', module: 'invoice' },
  '/invoices': { section: 'admin', module: 'invoice_admin' },
  '/system-info': { section: 'admin', module: 'system_info' },
  '/profile': { section: 'personal', module: 'personal' },
  '/channels': { section: 'admin', module: 'channel' },
  '/models': { section: 'admin', module: 'models' },
  '/models/metadata': { section: 'admin', module: 'models' },
  '/models/deployments': { section: 'admin', module: 'models' },
  '/users': { section: 'admin', module: 'user' },
  '/redemption-codes': { section: 'admin', module: 'redemption' },
  '/subscriptions': { section: 'admin', module: 'subscription' },
  '/system-settings': { section: 'admin', module: 'setting' },
  '/system-settings/site': { section: 'admin', module: 'setting' },
}

/**
 * Parse backend SidebarModulesAdmin configuration
 */
function parseSidebarConfig(
  value: string | null | undefined
): SidebarModulesAdminConfig {
  // If empty string, null, or undefined, use default config
  if (!value || value.trim() === '') {
    return DEFAULT_SIDEBAR_MODULES
  }

  try {
    const parsed = JSON.parse(value) as SidebarModulesAdminConfig
    return mergeWithDefaultSidebarModules(parsed)
  } catch {
    // eslint-disable-next-line no-console
    console.error('Failed to parse sidebar modules configuration')
    return DEFAULT_SIDEBAR_MODULES
  }
}

/**
 * Parse user-level sidebar_modules. Returns null when the value is empty,
 * invalid, or otherwise unusable — the caller treats null as "do not narrow",
 * so legacy users with an empty sidebar_modules field keep the full admin view.
 */
function parseUserSidebarConfig(
  value: string | null | undefined
): SidebarModulesUserConfig {
  if (!value || value.trim() === '') {
    return null
  }
  try {
    const parsed = JSON.parse(value) as SidebarModulesAdminConfig
    if (!parsed || typeof parsed !== 'object') return null
    return parsed
  } catch {
    return null
  }
}

/**
 * Check if a module is enabled. Admin config is the first (authoritative)
 * layer: if admin disables a section/module it is always hidden. User config
 * is a second narrower layer: it can only further hide what admin allowed.
 * A null user config means "do not narrow" (legacy/empty users).
 */
function isModuleEnabled(
  url: string,
  adminConfig: SidebarModulesAdminConfig,
  userConfig: SidebarModulesUserConfig,
  invoiceEnabled = true
): boolean {
  if (!invoiceEnabled && INVOICE_NAVIGATION_URLS.has(url)) return false

  const mapping = URL_TO_CONFIG_MAP[url]
  if (!mapping) {
    // No mapping config, default to visible (e.g. system settings and new features)
    return true
  }

  const { section, module } = mapping
  const adminSection = adminConfig[section]
  const adminAllowed = Boolean(
    adminSection && adminSection.enabled && adminSection[module] === true
  )
  if (!adminAllowed) return false

  if (!userConfig) return true

  const userSection = userConfig[section]
  if (!userSection) return true
  if (userSection.enabled === false) return false
  return userSection[module] !== false
}

/**
 * Check if a navigation item should be visible
 */
function isNavItemVisible(
  item: NavItem,
  adminConfig: SidebarModulesAdminConfig,
  userConfig: SidebarModulesUserConfig,
  invoiceEnabled: boolean
): boolean {
  // Handle dynamic chat presets type — also runs the admin × user AND gate
  if ('type' in item && item.type === 'chat-presets') {
    const adminChat = adminConfig.chat
    const adminAllowed = Boolean(adminChat?.enabled && adminChat.chat === true)
    if (!adminAllowed) return false
    if (!userConfig) return true
    const userChat = userConfig.chat
    if (!userChat) return true
    if (userChat.enabled === false) return false
    return userChat.chat !== false
  }

  // Handle direct link type
  if ('url' in item && item.url) {
    const configUrls = item.configUrls ?? [item.url]
    return configUrls.some((url) =>
      isModuleEnabled(url as string, adminConfig, userConfig, invoiceEnabled)
    )
  }

  // Handle collapsible type (with sub-items)
  if ('items' in item && item.items) {
    // If has sub-items, show this collapsible item if at least one sub-item is visible
    return item.items.some((subItem) =>
      isModuleEnabled(
        subItem.url as string,
        adminConfig,
        userConfig,
        invoiceEnabled
      )
    )
  }

  return true
}

/**
 * Filter navigation items
 */
function filterNavItems(
  items: NavItem[],
  adminConfig: SidebarModulesAdminConfig,
  userConfig: SidebarModulesUserConfig,
  invoiceEnabled: boolean
): NavItem[] {
  const filteredItems = items
    .map((item) => {
      // If collapsible item, also filter its sub-items
      if ('items' in item && item.items) {
        const filteredSubItems = item.items.filter((subItem) =>
          isModuleEnabled(
            subItem.url as string,
            adminConfig,
            userConfig,
            invoiceEnabled
          )
        )

        return {
          ...item,
          items: filteredSubItems,
        }
      }
      return item
    })
    .filter((item) =>
      isNavItemVisible(item, adminConfig, userConfig, invoiceEnabled)
    )

  return filteredItems.sort((left, right) => {
    const leftOrder = getNavItemOrder(left, adminConfig)
    const rightOrder = getNavItemOrder(right, adminConfig)
    return leftOrder - rightOrder
  })
}

function getNavItemOrder(
  item: NavItem,
  adminConfig: SidebarModulesAdminConfig
): number {
  const urls: string[] = []
  if ('url' in item && item.url) {
    urls.push(String(item.url))
    if (item.configUrls) urls.push(...item.configUrls.map(String))
  }
  if ('items' in item && item.items) {
    urls.push(...item.items.map((subItem) => String(subItem.url)))
  }

  const orderIndexes: number[] = []
  for (const url of urls) {
    const mapping = URL_TO_CONFIG_MAP[url]
    if (!mapping) continue
    const section = adminConfig[mapping.section]
    if (!section) continue
    const moduleOrder = Object.keys(section).filter(
      (moduleKey) => moduleKey !== 'enabled'
    )
    const index = moduleOrder.indexOf(mapping.module)
    if (index >= 0) orderIndexes.push(index)
  }

  return orderIndexes.length > 0
    ? Math.min(...orderIndexes)
    : Number.MAX_SAFE_INTEGER
}

function getSidebarSectionOrder(
  group: NavGroup,
  adminConfig: SidebarModulesAdminConfig
): number {
  // The general group is the user-facing label for the persisted console
  // section; all other root groups use their stable id directly.
  const sectionKey = group.id === 'general' ? 'console' : group.id
  if (!sectionKey) return Number.MAX_SAFE_INTEGER
  const index = Object.keys(adminConfig).indexOf(sectionKey)
  return index >= 0 ? index : Number.MAX_SAFE_INTEGER
}

/** Apply administrator ordering, visibility, and feature gates to navigation. */
export function applySidebarNavigationConfig(
  navGroups: NavGroup[],
  adminConfig: SidebarModulesAdminConfig,
  userConfig: SidebarModulesUserConfig,
  invoiceEnabled: boolean
): NavGroup[] {
  return navGroups
    .map((group) => ({
      ...group,
      items: filterNavItems(
        group.items,
        adminConfig,
        userConfig,
        invoiceEnabled
      ),
    }))
    .filter((group) => group.items.length > 0)
    .sort(
      (left, right) =>
        getSidebarSectionOrder(left, adminConfig) -
        getSidebarSectionOrder(right, adminConfig)
    )
}

/**
 * Filter sidebar navigation groups by admin × user sidebar_modules config.
 *
 * Two layers, AND-combined:
 *   1. Admin (status.SidebarModulesAdmin) — authoritative, falls back to
 *      DEFAULT_SIDEBAR_MODULES when empty/invalid. Disabling here hides the
 *      item for everyone regardless of user preference.
 *   2. User (auth.user.sidebar_modules) — narrower overlay, null sentinel
 *      means "don't narrow". A section/module is only hidden if the user
 *      explicitly set it to false; undefined fields default to visible so
 *      legacy users with empty sidebar_modules keep the full admin view.
 *      The overlay is also skipped entirely when the backend tells us the
 *      user cannot configure sidebar_settings (e.g. root accounts), so a
 *      stale historical value cannot lock them out of entries they have no
 *      UI to restore.
 */
export function useSidebarConfig(navGroups: NavGroup[]): NavGroup[] {
  const { status } = useStatus()
  const { auth } = useAuthStore()
  const invoiceEnabled = useMemo(
    () => isInvoiceFeatureEnabled(status as Record<string, unknown> | null),
    [status]
  )

  const adminConfig = useMemo(
    () =>
      parseSidebarConfig(
        status?.SidebarModulesAdmin as string | null | undefined
      ),
    [status?.SidebarModulesAdmin]
  )

  const userConfig = useMemo(() => {
    // If the backend marks the user as unable to configure the sidebar
    // (e.g. root accounts), skip the user overlay entirely — a stale
    // historical sidebar_modules value from a previous role would otherwise
    // hide admin entries for someone who has no in-product UI to restore
    // them.
    if (auth?.user?.permissions?.sidebar_settings === false) {
      return null
    }
    return parseUserSidebarConfig(auth?.user?.sidebar_modules)
  }, [auth?.user?.permissions?.sidebar_settings, auth?.user?.sidebar_modules])

  const filteredNavGroups = useMemo(
    () =>
      applySidebarNavigationConfig(
        navGroups,
        adminConfig,
        userConfig,
        invoiceEnabled
      ),
    [navGroups, adminConfig, userConfig, invoiceEnabled]
  )

  return filteredNavGroups
}

/**
 * Check whether a single route is visible under the current sidebar_modules
 * config. Used by entries living outside the sidebar (e.g. the profile
 * dropdown's wallet link) so they honour the same "wallet display" toggle.
 */
export function useIsSidebarModuleVisible(url: string): boolean {
  const { status } = useStatus()
  const { auth } = useAuthStore()

  const adminConfig = parseSidebarConfig(
    status?.SidebarModulesAdmin as string | null | undefined
  )
  const userConfig =
    auth?.user?.permissions?.sidebar_settings === false
      ? null
      : parseUserSidebarConfig(auth?.user?.sidebar_modules)

  return isModuleEnabled(
    url,
    adminConfig,
    userConfig,
    isInvoiceFeatureEnabled(status as Record<string, unknown> | null)
  )
}
