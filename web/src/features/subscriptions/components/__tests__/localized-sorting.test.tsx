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
import { Window } from 'happy-dom'

import type { PlanRecord } from '../../types'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { SubscriptionsProvider } = await import('../subscriptions-provider')
const { SubscriptionsTable } = await import('../subscriptions-table')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: {
    en: { translation: {} },
    zh: { translation: {} },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function createPlan(id: number, title: string): PlanRecord {
  return {
    plan: {
      id,
      title,
      price_amount: 10,
      currency: 'USD',
      duration_unit: 'month',
      duration_value: 1,
      quota_reset_period: 'never',
      enabled: true,
      sort_order: id,
      allow_balance_pay: true,
      allow_wallet_overflow: true,
      max_purchase_per_user: 0,
      total_amount: 1000,
    },
  }
}

describe('localized subscription sorting', () => {
  after(() => {
    domWindow.close()
  })

  test('re-sorts by the visible title after a language change without mutating source data', async () => {
    const plans = [
      createPlan(1, '<tnt l="zh">赵套餐</tnt><tnt l="en">Alpha plan</tnt>'),
      createPlan(2, '<tnt l="zh">安套餐</tnt><tnt l="en">Zulu plan</tnt>'),
    ]
    const rawTitles = plans.map((record) => record.plan.title)
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    queryClient.setQueryData(['admin-subscription-plans', 0], plans)
    queryClient.setQueryData(['system-options'], { data: [] })

    const footer = document.createElement('div')
    footer.id = 'page-footer-container'
    document.body.append(footer)
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <SubscriptionsProvider>
              <SubscriptionsTable />
            </SubscriptionsProvider>
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    const planHeader = [...container.querySelectorAll('button')].find(
      (button) => button.textContent?.trim() === 'Plan'
    )
    assert.ok(planHeader)
    await act(async () => planHeader.click())
    const ascendingItem = [
      ...document.querySelectorAll('[role="menuitem"]'),
    ].find((item) => item.textContent?.trim() === 'Asc') as
      | HTMLElement
      | undefined
    assert.ok(ascendingItem)
    await act(async () => ascendingItem.click())

    const readTitles = () =>
      [...container.querySelectorAll('tbody tr')].map(
        (row) => row.querySelectorAll('td')[1]?.textContent?.trim() ?? ''
      )

    assert.deepEqual(readTitles(), ['Alpha plan', 'Zulu plan'])

    await act(async () => i18n.changeLanguage('zh'))

    assert.deepEqual(readTitles(), ['安套餐', '赵套餐'])
    assert.deepEqual(
      plans.map((record) => record.plan.title),
      rawTitles
    )

    await act(async () => root.unmount())
    container.remove()
    footer.remove()
    queryClient.clear()
  })
})
