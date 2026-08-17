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
import { after, before, describe, test } from 'node:test'

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
const { SearchButton } = await import('../search')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: { Search: 'Search' } } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('search trigger', () => {
  before(() => {
    document.body.replaceChildren()
  })

  after(() => {
    document.body.replaceChildren()
    domWindow.close()
  })

  test('renders an accessible icon button and invokes the open action', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    let opened = false

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <SearchButton label='Search' onOpen={() => (opened = true)} />
        </I18nextProvider>
      )
    })

    const searchButton = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Search"]'
    )
    assert.ok(searchButton)
    assert.equal(searchButton.getAttribute('title'), 'Search')
    assert.equal(searchButton.querySelector('svg') !== null, true)
    assert.equal(searchButton.classList.contains('size-8'), true)

    await act(async () => searchButton.click())
    assert.equal(opened, true)

    await act(async () => root.unmount())
    container.remove()
  })
})
