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
import { useTranslation } from 'react-i18next'

import type { TopNavLink } from '@/components/layout/types'
import { useStatus } from '@/hooks/use-status'
import {
  parseHeaderNavModulesFromStatus,
  type HeaderNavModules,
} from '@/lib/nav-modules'
import { useAuthStore } from '@/stores/auth-store'

export type { TopNavLink } from '@/components/layout/types'

export function buildTopNavLinks(
  modules: HeaderNavModules,
  docsLink: string | undefined,
  isAuthed: boolean,
  translate: (key: string) => string
): TopNavLink[] {
  const links: TopNavLink[] = []

  // Home is intentionally fixed and is not part of the administrator-managed
  // order. The remaining entries follow the configured ordered item list.
  if (modules?.home !== false) {
    links.push({ id: 'home', title: translate('Home'), href: '/' })
  }

  const builtinLinks: Record<
    string,
    {
      title: string
      href: string
      requiresAuth?: boolean
      external?: boolean
      newTab?: boolean
    }
  > = {
    console: { title: translate('Console'), href: '/dashboard' },
    pricing: {
      title: translate('Model Square'),
      href: '/pricing',
      requiresAuth: modules.pricing.requireAuth && !isAuthed,
    },
    rankings: {
      title: translate('Rankings'),
      href: '/rankings',
      requiresAuth: modules.rankings.requireAuth && !isAuthed,
    },
    docs: {
      title: translate('Docs'),
      href: docsLink || '/docs',
      external: Boolean(docsLink),
      // Preserve the historical docs behavior: external documentation opens
      // in a new tab even when no explicit item setting exists.
      newTab: Boolean(docsLink),
    },
    about: { title: translate('About'), href: '/about' },
  }

  modules.items.forEach((item) => {
    if (!item.visible || item.id === 'home') return
    const builtin = builtinLinks[item.id]
    if (builtin) {
      links.push({
        ...builtin,
        id: item.id,
        newTab: builtin.newTab ?? item.newTab,
      })
      return
    }

    links.push({
      id: item.id,
      title: item.title,
      href: item.url,
      external: /^https?:\/\//i.test(item.url),
      newTab: item.newTab,
    })
  })

  return links
}

/**
 * Generate top navigation links based on HeaderNavModules configuration from
 * backend `/api/status`.
 */
export function useTopNavLinks(): TopNavLink[] {
  const { t } = useTranslation()
  const { status } = useStatus()
  const { auth } = useAuthStore()

  const modules = useMemo(() => {
    return parseHeaderNavModulesFromStatus(
      status as Record<string, unknown> | null
    )
  }, [status])
  const docsLink: string | undefined = status?.docs_link as string | undefined
  const isAuthed = !!auth?.user

  return useMemo(
    () => buildTopNavLinks(modules, docsLink, isAuthed, t),
    [docsLink, isAuthed, modules, t]
  )
}
