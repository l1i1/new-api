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

/**
 * Single registration point for manageable sidebar modules.
 *
 * Everything derived from `SIDEBAR_MODULE_REGISTRY` — the URL→module filter
 * map, the default `SidebarModulesAdmin` config, and the labels rendered by
 * the `/system-settings/site/sidebar-modules` editor — is generated from this
 * list. Registering a new module (one entry here, or via
 * `registerSidebarModule` from feature code) automatically makes it visible,
 * hideable, and reorderable from the admin editor without touching other
 * files. Entries with no stable URL (e.g. the dynamic chat-presets item) are
 * still registered so they keep an editor label and a default config slot.
 */

export type SidebarModuleSectionId = 'chat' | 'console' | 'personal' | 'admin'

export type SidebarModuleDefinition = {
  /** Owning sidebar section */
  section: SidebarModuleSectionId
  /** Stable key persisted inside the `SidebarModulesAdmin` JSON option */
  key: string
  /** Navigation URLs governed by this module (exact match, may be empty) */
  urls: string[]
  /** i18n key used as the display title in the admin editor */
  title: string
  /** i18n key used as the display description in the admin editor */
  description: string
}

/** Section chrome for the admin editor; sections are the fixed skeleton. */
export const SIDEBAR_MODULE_SECTIONS: {
  id: SidebarModuleSectionId
  title: string
  description: string
}[] = [
  {
    id: 'chat',
    title: 'Chat area',
    description: 'Playground experiments and live conversations.',
  },
  {
    id: 'console',
    title: 'General',
    description: 'Dashboards, tokens, and usage analytics.',
  },
  {
    id: 'personal',
    title: 'Personal area',
    description: 'Wallet management and personal preferences.',
  },
  {
    id: 'admin',
    title: 'Admin area',
    description: 'Global configuration and administrative tools.',
  },
]

const SIDEBAR_MODULE_REGISTRY: SidebarModuleDefinition[] = [
  // chat
  {
    section: 'chat',
    key: 'playground',
    urls: ['/playground'],
    title: 'Playground',
    description: 'Experiment with prompts and models in real time.',
  },
  {
    // The live chat entry is the dynamic chat-presets nav item, so it has no
    // stable URL; the visibility gate for it lives in use-sidebar-config.
    section: 'chat',
    key: 'chat',
    urls: [],
    title: 'Chat',
    description: 'Access previous conversations and start new ones.',
  },
  // console
  {
    section: 'console',
    key: 'detail',
    urls: [
      '/dashboard',
      '/dashboard/overview',
      '/dashboard/models',
      '/dashboard/users',
    ],
    title: 'Dashboard',
    description: 'Aggregated usage metrics and trend charts.',
  },
  {
    section: 'console',
    key: 'token',
    urls: ['/keys'],
    title: 'Token management',
    description: 'Create, revoke, and audit API tokens.',
  },
  {
    section: 'console',
    key: 'log',
    urls: ['/usage-logs', '/usage-logs/common'],
    title: 'Usage logs',
    description: 'Detailed request logs for investigations.',
  },
  {
    section: 'console',
    key: 'audit',
    urls: ['/usage-logs/audit'],
    title: 'Audit Logs',
    description: 'Review account and administrative audit events.',
  },
  {
    section: 'console',
    key: 'midjourney',
    urls: ['/usage-logs/drawing'],
    title: 'Drawing logs',
    description: 'History of MjProxy-style image tasks.',
  },
  {
    section: 'console',
    key: 'task',
    urls: ['/usage-logs/task'],
    title: 'Task logs',
    description: 'Background job tracker for queued work.',
  },
  // personal
  {
    section: 'personal',
    key: 'topup',
    urls: ['/wallet'],
    title: 'Wallet',
    description: 'Top up balance and view billing history.',
  },
  {
    section: 'personal',
    key: 'personal',
    urls: ['/profile'],
    title: 'Profile',
    description: 'Personal settings and profile management.',
  },
  {
    section: 'personal',
    key: 'security',
    urls: ['/security'],
    title: 'Security & Access',
    description: 'Manage your security settings and account access.',
  },
  {
    section: 'personal',
    key: 'invoice',
    urls: ['/invoice'],
    title: 'Invoices',
    description: 'Allow users to view and apply for invoices.',
  },
  // admin
  {
    section: 'admin',
    key: 'channel',
    urls: ['/channels'],
    title: 'Channels',
    description: 'Configure upstream providers and routing.',
  },
  {
    section: 'admin',
    key: 'models',
    urls: ['/models', '/models/metadata', '/models/deployments'],
    title: 'Models',
    description: 'Manage catalog visibility and pricing.',
  },
  {
    section: 'admin',
    key: 'redemption',
    urls: ['/redemption-codes'],
    title: 'Redeem codes',
    description: 'Create and review invite or credit codes.',
  },
  {
    section: 'admin',
    key: 'user',
    urls: ['/users'],
    title: 'Users',
    description: 'Administer user accounts and roles.',
  },
  {
    section: 'admin',
    key: 'setting',
    urls: ['/system-settings', '/system-settings/site'],
    title: 'System settings',
    description: 'Advanced platform configuration.',
  },
  {
    section: 'admin',
    key: 'subscription',
    urls: ['/subscriptions'],
    title: 'Subscription Management',
    description: 'Manage subscription plans and pricing.',
  },
  {
    section: 'admin',
    key: 'invoice_admin',
    urls: ['/invoices'],
    title: 'Invoice Review',
    description: 'Review and process user invoice applications.',
  },
  {
    section: 'admin',
    key: 'system_info',
    urls: ['/system-info'],
    title: 'System Info',
    description:
      'Nodes reporting from this deployment and their latest heartbeat.',
  },
  {
    section: 'admin',
    key: 'task_plugins',
    urls: ['/task-plugins'],
    title: 'Task Plugins',
    description: 'Manage installed task plugins and the marketplace.',
  },
]

/**
 * Runtime registration hook for feature code. Call at module load (top level
 * of an eagerly-imported file) so the derived maps below pick the entry up
 * before the sidebar or the admin editor render. Re-registering the same
 * section + key replaces the previous definition in place, keeping the call
 * idempotent under HMR.
 */
export function registerSidebarModule(
  definition: SidebarModuleDefinition
): void {
  const index = SIDEBAR_MODULE_REGISTRY.findIndex(
    (entry) =>
      entry.section === definition.section && entry.key === definition.key
  )
  if (index >= 0) {
    SIDEBAR_MODULE_REGISTRY[index] = definition
  } else {
    SIDEBAR_MODULE_REGISTRY.push(definition)
  }
  urlMap = buildUrlMap()
}

export type SidebarModuleUrlMapping = {
  section: SidebarModuleSectionId
  module: string
}

const buildUrlMap = (): Record<string, SidebarModuleUrlMapping> =>
  Object.fromEntries(
    SIDEBAR_MODULE_REGISTRY.flatMap((module) =>
      module.urls.map(
        (url) => [url, { section: module.section, module: module.key }] as const
      )
    )
  )

let urlMap: Record<string, SidebarModuleUrlMapping> = buildUrlMap()

/** Exact-match lookup from a navigation URL to its registered module. */
export function getSidebarModuleForUrl(
  url: string
): SidebarModuleUrlMapping | undefined {
  return urlMap[url]
}

/** Registered module metadata, in registration (display) order. */
export function getSidebarModules(): readonly SidebarModuleDefinition[] {
  return SIDEBAR_MODULE_REGISTRY
}

/**
 * Default `SidebarModulesAdmin` shape derived from the registry: every
 * registered section enabled with all of its modules enabled, in registry
 * order. Returns a fresh object so callers never share mutable state.
 */
export function buildDefaultSidebarModules(): Record<
  string,
  { enabled: boolean } & Record<string, boolean>
> {
  const config: Record<string, { enabled: boolean } & Record<string, boolean>> =
    {}
  for (const section of SIDEBAR_MODULE_SECTIONS) {
    config[section.id] = { enabled: true }
  }
  for (const module of SIDEBAR_MODULE_REGISTRY) {
    config[module.section] ??= { enabled: true }
    config[module.section][module.key] = true
  }
  return config
}
