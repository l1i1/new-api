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
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider, createRouter } from '@tanstack/react-router'
import { StrictMode } from 'react'
import ReactDOM from 'react-dom/client'

import {
  captureAffiliateCode,
  urlWithAffiliateCode,
} from '@/features/auth/lib/affiliate-param'
import { installBuildMetadata } from '@/lib/build-metadata'
import { applyFaviconToDom } from '@/lib/dom-utils'
import '@/lib/dayjs'
import { initializeFrontendCache } from '@/lib/frontend-cache'
import { createAppQueryClient } from '@/lib/query-client'
import { getCachedSystemName, resolvePageTitle } from '@/lib/route-title'
import { readCachedStatus, statusQueryOptions } from '@/lib/status-query'
import { initializeThemeCustomizationDom } from '@/lib/theme-customization-storage'

import { DirectionProvider } from './context/direction-provider'
import { FontProvider } from './context/font-provider'
import { ThemeProvider } from './context/theme-provider'
import './i18n/config'
// Generated Routes
import { routeTree } from './routeTree.gen'

// Styles
import './styles/index.css'

// Ensure VChart theme is initialized before any chart mounts (prevents white default theme flash)
// VChart theme is driven by our ThemeProvider (html.light/html.dark) via per-chart `theme` prop.
initializeFrontendCache()
installBuildMetadata()
initializeThemeCustomizationDom()

const queryClient = createAppQueryClient(() => {
  void router.navigate({ to: '/500' })
})

// Create a new router instance
const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: 'intent',
  defaultPreloadStaleTime: 0,
})

// Register the router instance for type safety
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

// Route-aware tab title: the homepage keeps the server-injected title (the
// `<!--head-html-->` CustomHeadHTML region); every other path shows
// "<Page> - <System name>". The server title is captured before any rewrite
// so navigating back to the homepage restores it.
const serverInjectedTitle = document.title
router.subscribe('onResolved', () => {
  const title = resolvePageTitle(
    router.state.location.pathname,
    getCachedSystemName()
  )
  document.title = title ?? serverInjectedTitle

  // Invite codes are sticky for the whole visit: whatever this landing page
  // carried is persisted, and the URL keeps carrying it so the visitor's next
  // page (and any link they copy) still attributes the eventual registration.
  // The rewrite is a plain history update, so the router's own search params
  // and every route's search schema stay untouched.
  const code = captureAffiliateCode(window.location.search)
  const next = urlWithAffiliateCode(window.location.href, code)
  if (next) {
    window.history.replaceState(window.history.state, '', next)
  }
})

// Render the app
const rootElement = document.querySelector<HTMLElement>('#root')
if (!rootElement) {
  throw new Error('Root element not found')
}
// Apply the favicon from cached status, then refresh from network. The
// document title and meta tags are owned by the server-rendered head
// (CustomHeadHTML option) and must not be rewritten here.
;(function initSystemBranding() {
  try {
    if (typeof window === 'undefined' || typeof document === 'undefined') return
    // Cache-first
    const cached = readCachedStatus()
    if (cached?.logo) applyFaviconToDom(cached.logo as string)

    // Background refresh through the shared cache. This primes ['status']
    // before React mounts, so every status consumer reuses the same request.
    queryClient
      .ensureQueryData(statusQueryOptions)
      .then((s) => {
        if (s?.logo) applyFaviconToDom(s.logo as string)
      })
      .catch(() => {
        /* empty */
      })
  } catch {
    /* empty */
  }
})()
if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement)
  root.render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <ThemeProvider>
          <FontProvider>
            <DirectionProvider>
              <RouterProvider router={router} />
            </DirectionProvider>
          </FontProvider>
        </ThemeProvider>
      </QueryClientProvider>
    </StrictMode>
  )
}
