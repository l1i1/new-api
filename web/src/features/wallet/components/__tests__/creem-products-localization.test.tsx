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

import { Window } from 'happy-dom'

import type { CreemProduct } from '../../types'

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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'zh',
  fallbackLng: 'en',
  resources: {
    en: { translation: { Quota: 'Quota' } },
    zh: { translation: { Quota: '额度' } },
  },
})

const { CreemProductsSection } = await import('../creem-products-section')
const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const product: CreemProduct = {
  name: '<tnt l="zh">基础套餐</tnt><tnt l="en">Starter</tnt>',
  productId: 'starter-product',
  price: 10,
  quota: 1000,
  currency: 'USD',
}

type RenderedProducts = {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}

async function renderProducts(
  onProductSelect: (selected: CreemProduct) => void
): Promise<RenderedProducts> {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <I18nextProvider i18n={i18n}>
        <CreemProductsSection
          products={[product]}
          onProductSelect={onProductSelect}
        />
      </I18nextProvider>
    )
  })

  return { container, root }
}

async function unmountProducts(rendered: RenderedProducts) {
  await act(async () => rendered.root.unmount())
  rendered.container.remove()
}

describe('Creem product localization', () => {
  after(() => {
    domWindow.close()
  })

  test('renders the active tnt translation while preserving the raw product', async () => {
    const selectedProducts: CreemProduct[] = []
    const rendered = await renderProducts((selected) =>
      selectedProducts.push(selected)
    )

    assert.equal(rendered.container.textContent?.includes('基础套餐'), true)
    assert.equal(rendered.container.textContent?.includes('<tnt'), false)

    const productName = [...rendered.container.querySelectorAll('div')].find(
      (element) => element.textContent === '基础套餐'
    )
    assert.ok(productName)

    await act(async () => productName.click())
    assert.deepEqual(selectedProducts, [product])

    await act(async () => i18n.changeLanguage('en'))
    assert.equal(rendered.container.textContent?.includes('Starter'), true)

    await unmountProducts(rendered)
  })
})
