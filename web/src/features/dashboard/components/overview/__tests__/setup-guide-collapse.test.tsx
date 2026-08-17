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
import assert from 'node:assert/strict'
import { after, afterEach, describe, test } from 'node:test'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { Window } from 'happy-dom'

import en from '@/i18n/locales/en.json'

const SETUP_GUIDE_VISIBILITY_STORAGE_KEY =
  'dashboard_overview_setup_guide_expanded'

const domWindow = new Window({
  url: 'https://tokeness.test/dashboard/overview',
})
domWindow.document.write(
  '<!doctype html><html><head></head><body></body></html>'
)
Object.defineProperty(domWindow.document, 'compatMode', {
  configurable: true,
  value: 'CSS1Compat',
})
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLButtonElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'MouseEvent',
  'PointerEvent',
  'KeyboardEvent',
  'CustomEvent',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

Object.defineProperty(domWindow.Element.prototype, 'getAnimations', {
  configurable: true,
  value: () => [],
})
Object.defineProperty(domWindow, 'matchMedia', {
  configurable: true,
  value: () => ({
    matches: false,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }),
})
Object.defineProperty(globalThis, 'scrollTo', {
  configurable: true,
  value: () => undefined,
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { useAuthStore } = await import('@/stores/auth-store')
const { OverviewDashboard } = await import('../overview-dashboard')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function setUser(requestCount: number) {
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'tester',
    role: 1,
    quota: 100,
    used_quota: 0,
    request_count: requestCount,
  })
}

async function renderDashboard() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { enabled: false, retry: false, staleTime: Infinity },
    },
  })
  queryClient.setQueryData(
    ['dashboard', 'overview', 'api-keys'],
    [
      {
        id: 1,
        name: 'Test key',
        key: 'test-key',
        status: 1,
        remain_quota: 100,
        used_quota: 0,
        unlimited_quota: false,
        expired_time: -1,
        created_time: 1,
        accessed_time: 0,
        group: 'default',
        auto_groups: null,
        cross_group_retry: false,
        model_limits_enabled: false,
        model_limits: '',
        allow_ips: '',
      },
    ]
  )
  queryClient.setQueryData(
    ['dashboard', 'overview', 'user-models'],
    ['gpt-4o-mini']
  )
  queryClient.setQueryData(['status'], {
    api_info_enabled: false,
    announcements_enabled: false,
    faq_enabled: false,
    uptime_kuma_enabled: false,
  })

  const rootRoute = createRootRoute()
  const overviewRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/dashboard/overview',
    component: OverviewDashboard,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([overviewRoute]),
    history: createMemoryHistory({
      initialEntries: ['/dashboard/overview'],
    }),
  })
  await router.load()

  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <RouterProvider router={router} />
        </I18nextProvider>
      </QueryClientProvider>
    )
  })

  return { container, queryClient, root }
}

function findButton(root: ParentNode, label: string) {
  return [...root.querySelectorAll<HTMLButtonElement>('button')].find(
    (button) => button.textContent?.trim() === label
  )
}

async function cleanupDashboard(
  rendered: Awaited<ReturnType<typeof renderDashboard>>
) {
  await act(async () => rendered.root.unmount())
  rendered.container.remove()
  rendered.queryClient.clear()
}

describe('overview setup guide completion', () => {
  afterEach(() => {
    useAuthStore.getState().auth.reset()
    window.localStorage.clear()
  })

  after(() => {
    domWindow.close()
  })

  test('collapses a saved-expanded guide when the first loaded status is complete', async () => {
    window.localStorage.setItem(SETUP_GUIDE_VISIBILITY_STORAGE_KEY, 'expanded')
    setUser(1)

    const rendered = await renderDashboard()

    assert.equal(
      rendered.container.textContent?.includes('Setup guide complete'),
      true
    )
    assert.equal(findButton(rendered.container, 'Hide setup guide'), undefined)
    assert.equal(
      window.localStorage.getItem(SETUP_GUIDE_VISIBILITY_STORAGE_KEY),
      'collapsed'
    )

    const showButton = findButton(rendered.container, 'Show setup guide')
    assert.ok(showButton)
    await act(async () => showButton.click())

    assert.ok(findButton(rendered.container, 'Hide setup guide'))
    assert.equal(
      window.localStorage.getItem(SETUP_GUIDE_VISIBILITY_STORAGE_KEY),
      'expanded'
    )

    await cleanupDashboard(rendered)
  })

  test('collapses an expanded guide when the final setup step completes', async () => {
    window.localStorage.setItem(SETUP_GUIDE_VISIBILITY_STORAGE_KEY, 'expanded')
    setUser(0)

    const rendered = await renderDashboard()
    assert.ok(findButton(rendered.container, 'Hide setup guide'))

    await act(async () => setUser(1))

    assert.equal(
      rendered.container.textContent?.includes('Setup guide complete'),
      true
    )
    assert.ok(findButton(rendered.container, 'Show setup guide'))
    assert.equal(
      window.localStorage.getItem(SETUP_GUIDE_VISIBILITY_STORAGE_KEY),
      'collapsed'
    )

    await cleanupDashboard(rendered)
  })
})
