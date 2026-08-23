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
*/
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window({ url: 'https://tokeness.test/pricing/model' })
domWindow.document.insertBefore(
  domWindow.document.implementation.createDocumentType('html', '', ''),
  domWindow.document.documentElement
)
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
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
const { DynamicPricingBreakdown } = await import('../dynamic-pricing-breakdown')

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

const billingExpr =
  'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 ? tier("pro_peak", p * 1 + c * 2) : tier("pro_offpeak", p * 0.5 + c * 1)'

async function renderBreakdown(compact = false) {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <I18nextProvider i18n={i18n}>
        <DynamicPricingBreakdown
          billingExpr={billingExpr}
          compact={compact}
          displayCurrency='USD'
        />
      </I18nextProvider>
    )
  })

  return { container, root }
}

describe('dynamic pricing breakdown expression display', () => {
  after(() => {
    domWindow.close()
  })

  test('shows the billing expression in the model detail layout', async () => {
    const { container, root } = await renderBreakdown()

    assert.ok(container.textContent?.includes(billingExpr))

    await act(async () => root.unmount())
    container.remove()
  })

  test('keeps the compact usage-log layout free of the billing expression', async () => {
    const { container, root } = await renderBreakdown(true)

    assert.equal(container.textContent?.includes(billingExpr), false)

    await act(async () => root.unmount())
    container.remove()
  })
})
