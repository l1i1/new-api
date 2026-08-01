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

const domWindow = new Window({ url: 'https://tokeness.test/admin/users' })
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
const { UserModelRateLimitsSection } =
  await import('../user-model-rate-limits-section')

const originalApiGet = api.get
const originalApiPut = api.put
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

describe('user model rate limits', () => {
  afterEach(() => {
    api.get = originalApiGet
    api.put = originalApiPut
    document.body.replaceChildren()
  })

  after(() => domWindow.close())

  test('loads rules and submits the complete edited rule list', async () => {
    api.get = (async () => ({
      data: {
        success: true,
        data: [
          {
            id: 1,
            user_id: 42,
            model_name: 'gpt-test',
            window_seconds: 60,
            max_requests: 10,
            enabled: true,
          },
        ],
      },
    })) as typeof api.get
    let savedRules: unknown
    api.put = (async (_url, payload) => {
      savedRules = payload
      return {
        data: {
          success: true,
          data: (payload as { rules: unknown[] }).rules,
        },
      }
    }) as typeof api.put

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    await queryClient.prefetchQuery({
      queryKey: ['user-model-rate-limits', 42],
      queryFn: () =>
        api.get('/api/user/42/model-rate-limits').then((res) => res.data),
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <UserModelRateLimitsSection userId={42} />
          </I18nextProvider>
        </QueryClientProvider>
      )
    })
    assert.equal(
      document.querySelector<HTMLInputElement>('#model-rate-limit-model-0')
        ?.value,
      'gpt-test'
    )
    await act(async () => findButton('Add rule').click())
    const secondModel = document.querySelector<HTMLInputElement>(
      '#model-rate-limit-model-1'
    )
    assert.ok(secondModel)
    await act(async () => {
      const valueSetter = Object.getOwnPropertyDescriptor(
        domWindow.HTMLInputElement.prototype,
        'value'
      )?.set
      assert.ok(valueSetter)
      valueSetter.call(secondModel, 'gpt-second')
      secondModel.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => findButton('Save model limits').click())

    assert.deepEqual(savedRules, {
      rules: [
        {
          model_name: 'gpt-test',
          window_seconds: 60,
          max_requests: 10,
          enabled: true,
        },
        {
          model_name: 'gpt-second',
          window_seconds: 60,
          max_requests: 60,
          enabled: true,
        },
      ],
    })
    await act(async () => root.unmount())
    queryClient.clear()
  })
})
