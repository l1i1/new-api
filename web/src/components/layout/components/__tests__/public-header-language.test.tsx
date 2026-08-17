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
import { after, describe, test } from 'node:test'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { Window } from 'happy-dom'

const domWindow = new Window({ url: 'https://tokeness.test/' })
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
  'HTMLDivElement',
  'HTMLTemplateElement',
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
  'ShadowRoot',
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
const { useCurrencyDisplayStore } =
  await import('@/stores/currency-display-store')
const { useSystemConfigStore } = await import('@/stores/system-config-store')
const { PublicHeader } = await import('../public-header')
const originalDisplayCurrency = useCurrencyDisplayStore.getState().currency

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: {
    en: {
      translation: {
        'Change language': 'Change language',
        'Toggle navigation menu': 'Toggle navigation menu',
      },
    },
    zhCN: {
      translation: {
        'Change language': '切换语言',
        'Toggle navigation menu': '切换导航菜单',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function findButton(root: ParentNode, name: string): HTMLButtonElement | null {
  return (
    [...root.querySelectorAll('button')].find(
      (button) =>
        button.getAttribute('aria-label') === name ||
        button.textContent?.trim() === name
    ) ?? null
  )
}

async function renderHeader(
  showLanguageSwitcher = true,
  showNotifications = false
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  queryClient.setQueryData(['notice'], { success: true, data: '' })
  queryClient.setQueryData(['status'], {
    announcements_enabled: false,
    announcements: [],
  })
  const rootRoute = createRootRoute()
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => (
      <PublicHeader
        showAuthButtons={false}
        showLanguageSwitcher={showLanguageSwitcher}
        showNotifications={showNotifications}
        showThemeSwitch={false}
        siteName='Tokeness'
      />
    ),
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([homeRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
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

describe('public header language controls', () => {
  after(() => {
    useCurrencyDisplayStore.getState().setCurrency(originalDisplayCurrency)
    domWindow.close()
  })

  test('mobile actions expose a working language switcher', async () => {
    useAuthStore.getState().auth.reset()
    useCurrencyDisplayStore.getState().setCurrency('CNY')
    useSystemConfigStore.getState().setLoading(false)
    await i18n.changeLanguage('en')
    const rendered = await renderHeader()
    const menuButton = findButton(rendered.container, 'Toggle navigation menu')
    assert.ok(menuButton)
    const languageButton = findButton(
      menuButton.parentElement ?? rendered.container,
      'Change language'
    )
    assert.ok(languageButton, 'mobile actions must expose language selection')
    const currencyButton = findButton(
      menuButton.parentElement ?? rendered.container,
      'Currency'
    )
    assert.ok(currencyButton, 'mobile actions must expose currency selection')

    await act(async () => languageButton.click())
    const chineseOption = [
      ...document.querySelectorAll<HTMLElement>('[role="menuitem"]'),
    ].find((item) => item.textContent?.includes('简体中文'))
    assert.ok(chineseOption)

    await act(async () => chineseOption.click())
    assert.equal(i18n.language, 'zhCN')

    await act(async () => currencyButton.click())
    const usdOption = [
      ...document.querySelectorAll<HTMLElement>('[role="menuitem"]'),
    ].find((item) => item.textContent?.includes('$ USD'))
    assert.ok(usdOption)
    await act(async () => usdOption.click())
    assert.equal(useCurrencyDisplayStore.getState().currency, 'USD')

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
    rendered.queryClient.clear()
  })

  test('showLanguageSwitcher=false removes desktop and mobile controls', async () => {
    await i18n.changeLanguage('en')
    const rendered = await renderHeader(false)

    assert.equal(findButton(rendered.container, 'Change language'), null)
    assert.equal(findButton(rendered.container, 'Currency'), null)

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
    rendered.queryClient.clear()
  })

  test('mobile actions expose an accessible notification entry', async () => {
    await i18n.changeLanguage('en')
    const rendered = await renderHeader(true, true)
    const notificationButton = findButton(rendered.container, 'Notifications')
    assert.ok(notificationButton)

    await act(async () => notificationButton.click())
    assert.equal(
      document.body.textContent?.includes('System Announcements'),
      true
    )

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
    rendered.queryClient.clear()
  })
})
