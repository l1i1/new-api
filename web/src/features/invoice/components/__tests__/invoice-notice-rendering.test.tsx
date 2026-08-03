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

import { Window } from 'happy-dom'
import type React from 'react'

const domWindow = new Window({ url: 'https://tokeness.test/invoice' })
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
  'localStorage',
  'HTMLElement',
  'HTMLTemplateElement',
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
const { InvoiceNotice } = await import('../invoice-apply-panel')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'zh',
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

type RenderedComponent = {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}

async function renderWithI18n(element: React.ReactElement) {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(<I18nextProvider i18n={i18n}>{element}</I18nextProvider>)
  })

  return { container, root }
}

async function unmountComponent(rendered: RenderedComponent) {
  await act(async () => rendered.root.unmount())
  rendered.container.remove()
}

describe('invoice notice rendering', () => {
  afterEach(async () => {
    await i18n.changeLanguage('zh')
  })

  after(() => {
    domWindow.close()
  })

  test('renders localized Markdown without exposing syntax', async () => {
    const notice = [
      '<tnt l="zh">## 开票须知\n- 核对实际支付金额\n- 填写完整信息</tnt>',
      '<tnt l="en">## Invoice Notes\n- Verify the paid amount\n- Complete all fields</tnt>',
    ].join('\n')
    const rendered = await renderWithI18n(<InvoiceNotice notice={notice} />)

    assert.equal(rendered.container.textContent?.includes('开票须知'), true)
    assert.equal(rendered.container.textContent?.includes('##'), false)
    assert.deepEqual(
      [...rendered.container.querySelectorAll('li')].map(
        (item) => item.textContent
      ),
      ['核对实际支付金额', '填写完整信息']
    )

    await act(async () => i18n.changeLanguage('en'))

    assert.equal(
      rendered.container.textContent?.includes('Invoice Notes'),
      true
    )
    assert.equal(rendered.container.textContent?.includes('开票须知'), false)
    assert.deepEqual(
      [...rendered.container.querySelectorAll('li')].map(
        (item) => item.textContent
      ),
      ['Verify the paid amount', 'Complete all fields']
    )

    await unmountComponent(rendered)
  })
})
