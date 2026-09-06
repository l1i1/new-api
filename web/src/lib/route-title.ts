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
import i18next from 'i18next'

// The homepage keeps the server-injected title (the `<!--head-html-->`
// CustomHeadHTML region); every other path gets "<Page> - <System name>".
export const HOME_PATH = '/'

// Localized page names reuse the sidebar nav translation keys, so the tab
// title always matches the label users see in the navigation. i18next falls
// back to the key itself (English) when a locale is missing an entry.
const PAGE_TITLE_RULES: ReadonlyArray<{ prefix: string; key: string }> = [
  { prefix: '/playground', key: 'Playground' },
  { prefix: '/dashboard/overview', key: 'Overview' },
  { prefix: '/dashboard', key: 'Dashboard' },
  { prefix: '/keys', key: 'API Keys' },
  { prefix: '/usage-logs/task', key: 'Task Logs' },
  { prefix: '/usage-logs', key: 'Usage Logs' },
  { prefix: '/wallet', key: 'Wallet' },
  { prefix: '/invoices', key: 'Invoice Review' },
  { prefix: '/invoice', key: 'Invoices' },
  { prefix: '/profile', key: 'Profile' },
  { prefix: '/channels', key: 'Channels' },
  { prefix: '/models', key: 'Models' },
  { prefix: '/users', key: 'Users' },
  { prefix: '/redemption-codes', key: 'Redemption Codes' },
  { prefix: '/subscriptions', key: 'Subscriptions' },
  { prefix: '/system-info', key: 'System Info' },
  { prefix: '/task-plugins', key: 'Task Plugins' },
  { prefix: '/system-settings', key: 'System Settings' },
  { prefix: '/chat2link', key: 'Chat' },
  { prefix: '/chat', key: 'Chat' },
  { prefix: '/about', key: 'About' },
  { prefix: '/pricing', key: 'Model Square' },
  { prefix: '/rankings', key: 'Rankings' },
  { prefix: '/privacy-policy', key: 'Privacy Policy' },
  { prefix: '/user-agreement', key: 'User Agreement' },
  { prefix: '/sign-in', key: 'Sign In' },
  { prefix: '/sign-up', key: 'Sign Up' },
  { prefix: '/forgot-password', key: 'Forgot Password' },
  { prefix: '/reset', key: 'Reset Password' },
  { prefix: '/setup', key: 'Setup' },
]

// Shown before /api/status (or the cached copy) has delivered system_name.
const FALLBACK_SYSTEM_NAME = 'Tokeness'

function matchesPrefix(pathname: string, prefix: string): boolean {
  // Strict: the prefix must be the whole path or a full segment boundary, so
  // "/invoices" never matches the "/invoice" rule.
  return pathname === prefix || pathname.startsWith(`${prefix}/`)
}

function prettifySegment(segment: string): string {
  return segment
    .split(/[-_]/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ')
}

/**
 * Resolve the tab title for a location. Returns `null` for the homepage so
 * the caller leaves the server-injected `<!--head-html-->` title untouched.
 * Unknown paths fall back to a prettified last segment ("页面名 - 系统名").
 */
export function resolvePageTitle(
  pathname: string,
  systemName?: string | null
): string | null {
  if (!pathname || pathname === HOME_PATH) return null

  const name = systemName?.trim() || FALLBACK_SYSTEM_NAME
  const rule = PAGE_TITLE_RULES.find((candidate) =>
    matchesPrefix(pathname, candidate.prefix)
  )
  if (rule) return `${i18next.t(rule.key)} - ${name}`
  // Unknown path: prettify the last segment ("/oauth-callback" → "Oauth Callback").
  const segments = pathname.split('/').filter(Boolean)
  const last = segments.at(-1) ?? ''
  return `${prettifySegment(last)} - ${name}`
}

/**
 * Best-effort system name from the cached /api/status payload (written by the
 * status store); returns null when nothing usable is cached.
 */
export function getCachedSystemName(): string | null {
  try {
    const saved = localStorage.getItem('status')
    if (!saved) return null
    const parsed: unknown = JSON.parse(saved)
    if (typeof parsed !== 'object' || parsed === null) return null
    const direct = (parsed as { system_name?: unknown }).system_name
    if (typeof direct === 'string' && direct.trim()) return direct
    const nested = (parsed as { data?: { system_name?: unknown } }).data
      ?.system_name
    return typeof nested === 'string' && nested.trim() ? nested : null
  } catch {
    return null
  }
}
