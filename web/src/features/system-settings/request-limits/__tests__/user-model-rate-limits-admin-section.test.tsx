/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

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

import { Window } from 'happy-dom'

const domWindow = new Window({
  url: 'https://tokeness.test/admin/system-settings/security/rate-limit',
})
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLButtonElement',
  'HTMLInputElement',
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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { api } = await import('@/lib/api')
const { UserModelRateLimitsAdminSection } =
  await import('../user-model-rate-limits-admin-section')

const originalApiGet = api.get
const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function findButton(name: string): HTMLButtonElement {
  const button = [...document.querySelectorAll('button')].find(
    (candidate) =>
      candidate.getAttribute('aria-label') === name ||
      candidate.textContent?.trim() === name
  )
  assert.ok(button, `Expected button ${name}`)
  return button
}

describe('user model rate limits admin section', () => {
  afterEach(() => {
    api.get = originalApiGet
    document.body.replaceChildren()
  })

  after(() => domWindow.close())

  test('searches users, selects one, and renders their model rate limits', async () => {
    api.get = (async (url) => {
      if (String(url).startsWith('/api/user/search')) {
        return {
          data: {
            success: true,
            data: {
              items: [
                {
                  id: 42,
                  username: 'alice',
                  display_name: '',
                  role: 1,
                  status: 1,
                  quota: 0,
                  used_quota: 0,
                  request_count: 0,
                  group: 'default',
                },
              ],
              total: 1,
            },
          },
        }
      }
      if (String(url) === '/api/user/42/model-rate-limits') {
        return { data: { success: true, data: [] } }
      }
      return { data: { success: true, data: null } }
    }) as typeof api.get

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    await queryClient.prefetchQuery({
      queryKey: ['admin-user-model-rate-limit-search', ''],
      queryFn: () =>
        api.get('/api/user/search?keyword=&p=1&page_size=20').then((res) => res.data),
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <UserModelRateLimitsAdminSection />
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    assert.ok(
      document.body.textContent?.includes('User model rate limits')
    )
    await act(async () => findButton('#42 alice').click())
    assert.ok(document.body.textContent?.includes('Model Rate Limits'))
    assert.ok(document.body.textContent?.includes('Change user'))
    await act(async () => findButton('Change user').click())
    assert.ok(
      document.querySelector<HTMLInputElement>('#user-model-rate-limit-search')
    )

    await act(async () => root.unmount())
    queryClient.clear()
  })
})
