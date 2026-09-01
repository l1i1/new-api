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

import type { PricingModel } from '../../types'

const domWindow = new Window({ url: 'https://tokeness.test/pricing' })
domWindow.document.insertBefore(
  domWindow.document.implementation.createDocumentType('html', '', ''),
  domWindow.document.documentElement
)
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
  'customElements',
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

const matchMedia = () => ({
  matches: false,
  addEventListener: () => undefined,
  removeEventListener: () => undefined,
})
Object.defineProperty(domWindow, 'matchMedia', {
  configurable: true,
  value: matchMedia,
})
Object.defineProperty(globalThis, 'matchMedia', {
  configurable: true,
  value: matchMedia,
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { api } = await import('@/lib/api')
const { ModelCardGrid } = await import('../model-card-grid')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: { seconds: 'translated-seconds' } } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const models: PricingModel[] = Array.from({ length: 31 }, (_, index) => ({
  id: index + 1,
  model_name: `model-${index + 1}`,
  quota_type: 1,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
}))

function countCards(container: ParentNode): number {
  return container.querySelectorAll('[role="button"]').length
}

describe('model card grid pagination', () => {
  const originalApiGet = api.get

  afterEach(() => {
    api.get = originalApiGet
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('renders task price ranges, units, and schema labels', async () => {
    api.get = (async () => ({
      data: { data: { models: [] } },
    })) as typeof api.get
    const taskModel: PricingModel = {
      id: 32,
      model_name: 'task-tiered-model',
      quota_type: 0,
      model_ratio: 1,
      completion_ratio: 1,
      enable_groups: ['default'],
      group_ratio: { default: 1 },
      billing_mode: 'tiered_expr',
      billing_expr:
        'u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)',
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
        mode: { enum: ['std', 'pro'] },
      },
    }
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const queryClient = new QueryClient()

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <ModelCardGrid
              models={[taskModel]}
              onModelClick={() => undefined}
              displayCurrency='CNY'
              usdExchangeRate={7}
              selectedGroup='default'
            />
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    assert.ok(container.textContent?.includes('seconds'))
    assert.ok(!container.textContent?.includes('translated-seconds'))
    assert.ok(container.textContent?.includes('¥2.8 – ¥5.6'))
    assert.ok(container.textContent?.includes('/ s'))

    await act(async () => root.unmount())
    queryClient.clear()
  })

  test('changes the number of visible models from the pagination control', async () => {
    api.get = (async () => ({
      data: { data: { models: [] } },
    })) as typeof api.get

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <ModelCardGrid models={models} onModelClick={() => undefined} />
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    assert.equal(countCards(container), 30)
    const pageSizeTrigger = container.querySelector<HTMLButtonElement>(
      'button[data-slot="select-trigger"]'
    )
    assert.ok(pageSizeTrigger)
    assert.equal(pageSizeTrigger.getAttribute('aria-label'), 'Rows per page')

    await act(async () => pageSizeTrigger.click())
    const tenOption = [
      ...document.querySelectorAll<HTMLElement>('[data-slot="select-item"]'),
    ].find((option) => option.textContent?.trim() === '10')
    assert.ok(tenOption)
    await act(async () => tenOption.click())

    assert.equal(countCards(container), 10)
    const updatedPageSizeTrigger = container.querySelector<HTMLButtonElement>(
      'button[data-slot="select-trigger"]'
    )
    assert.ok(updatedPageSizeTrigger)
    assert.equal(updatedPageSizeTrigger.textContent?.includes('10'), true)

    await act(async () => root.unmount())
    queryClient.clear()
  })
})
