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

import type { CellContext } from '@tanstack/react-table'
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
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { usePricingColumns } = await import('../pricing-columns')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} } },
})

const dynamicModel: PricingModel = {
  id: 1,
  model_name: 'tiered-test',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
  group_ratio: { default: 0.3 },
  billing_mode: 'tiered_expr',
  billing_expr: 'tier("base", p * 1 + c * 2)',
}

const taskModel: PricingModel = {
  ...dynamicModel,
  id: 2,
  model_name: 'task-tiered-test',
  group_ratio: { default: 1 },
  billing_expr:
    'u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)',
  billing_usage_schema: {
    seconds: { type: 'number', unit: 'second' },
    mode: { enum: ['std', 'pro'] },
  },
}

function PricingCellProbe(props: {
  model: PricingModel
  groupRatioMultiplier?: number
}) {
  const columns = usePricingColumns({
    displayCurrency: 'CNY',
    usdExchangeRate: 7,
    selectedGroup: props.groupRatioMultiplier === 1 ? 'default' : undefined,
  })
  const priceColumn = columns.find(
    (column) => 'accessorKey' in column && column.accessorKey === 'price'
  )
  assert.ok(priceColumn)
  const priceCell = priceColumn.cell
  if (typeof priceCell !== 'function') {
    throw new TypeError(
      'Pricing price column must render through a cell function'
    )
  }

  const cellContext = {
    row: { original: props.model },
  } as unknown as CellContext<PricingModel, unknown>

  return <I18nextProvider i18n={i18n}>{priceCell(cellContext)}</I18nextProvider>
}

function renderProbe(model: PricingModel, groupRatioMultiplier?: number) {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  return act(async () => {
    root.render(
      <PricingCellProbe
        model={model}
        groupRatioMultiplier={groupRatioMultiplier}
      />
    )
    return { container, root }
  })
}

describe('pricing table dynamic price display', () => {
  after(() => {
    domWindow.close()
  })

  test('renders the original dynamic prices with a strikethrough for a discounted group', async () => {
    const { container, root } = await renderProbe(dynamicModel)

    const originalPrices = container.querySelectorAll('.line-through')
    assert.equal(originalPrices.length, 2)
    assert.deepEqual(
      [...originalPrices].map((element) => element.textContent),
      ['¥7', '¥14']
    )
    assert.ok(container.textContent?.includes('¥2.1'))
    assert.ok(container.textContent?.includes('¥4.2'))

    await act(async () => root.unmount())
    container.remove()
  })

  test('does not render an original price when the group ratio is one', async () => {
    const model = { ...dynamicModel, group_ratio: { default: 1 } }
    const { container, root } = await renderProbe(model, 1)

    assert.equal(container.querySelector('.line-through'), null)
    assert.ok(container.textContent?.includes('¥7'))
    assert.ok(container.textContent?.includes('¥14'))

    await act(async () => root.unmount())
    container.remove()
  })

  test('renders task tier ranges with their usage unit', async () => {
    const { container, root } = await renderProbe(taskModel, 1)

    assert.ok(container.textContent?.includes('¥2.8 – ¥5.6'))
    assert.ok(container.textContent?.includes('/ s'))

    await act(async () => root.unmount())
    container.remove()
  })
})
