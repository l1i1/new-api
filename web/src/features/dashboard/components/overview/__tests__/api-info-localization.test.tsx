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

import type { ApiInfoItem } from '@/features/dashboard/types'

const domWindow = new Window()
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
  'SVGElement',
  'Node',
  'Element',
  'Event',
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

Object.defineProperty(domWindow.Element.prototype, 'getAnimations', {
  configurable: true,
  value: () => [],
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { ApiInfoItemComponent } = await import('../api-info-item')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'zh',
  fallbackLng: 'en',
  resources: { en: { translation: {} }, zh: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('API info item localization', () => {
  after(() => {
    domWindow.close()
  })

  test('updates route and description when the language changes without mutating API data', async () => {
    const route = '<tnt l="zh">主节点</tnt><tnt l="en">Primary</tnt>'
    const description =
      '<tnt l="zh">全球加速</tnt><tnt l="en">Global acceleration</tnt>'
    const item: ApiInfoItem = {
      route,
      description,
      url: 'https://n.tokeness.io/<tnt l="en">raw</tnt>',
      color: 'cyan',
    }
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <ApiInfoItemComponent
            item={item}
            status={{ latency: null, testing: false, error: false }}
            onTest={() => undefined}
          />
        </I18nextProvider>
      )
    })

    assert.equal(container.textContent?.includes('主节点'), true)
    assert.equal(container.textContent?.includes('全球加速'), true)
    assert.equal(container.textContent?.includes('Primary'), false)

    await act(async () => i18n.changeLanguage('en'))

    assert.equal(container.textContent?.includes('Primary'), true)
    assert.equal(container.textContent?.includes('Global acceleration'), true)
    assert.equal(container.textContent?.includes('主节点'), false)
    assert.equal(
      container.textContent?.includes(
        'https://n.tokeness.io/<tnt l="en">raw</tnt>'
      ),
      true
    )
    assert.equal(item.route, route)
    assert.equal(item.description, description)

    await act(async () => root.unmount())
    container.remove()
  })

  test('compact items expose the URL and a description tooltip', async () => {
    const item: ApiInfoItem = {
      route: 'Primary',
      description: 'Global acceleration',
      url: 'https://api.example.com/v1',
      color: 'cyan',
    }
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const copiedValues: string[] = []

    Object.defineProperty(domWindow.navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: async (value: string) => {
          copiedValues.push(value)
        },
      },
    })

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <ApiInfoItemComponent
            item={item}
            status={{ latency: null, testing: false, error: false }}
            onTest={() => undefined}
            compact
          />
        </I18nextProvider>
      )
    })

    assert.equal(container.textContent?.includes(item.url), true)
    assert.equal(container.textContent?.includes('Global acceleration'), false)
    assert.equal(
      container.querySelector('[data-slot="tooltip-trigger"]')?.textContent,
      item.route
    )
    const descriptionTrigger = container.querySelector<HTMLButtonElement>(
      'button[data-slot="tooltip-trigger"]'
    )
    assert.ok(descriptionTrigger)
    await act(async () => descriptionTrigger.focus())
    assert.equal(document.activeElement, descriptionTrigger)
    assert.equal(
      descriptionTrigger.getAttribute('aria-label'),
      'Primary: Global acceleration'
    )
    const copyButton = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Copy URL: Primary"]'
    )
    assert.ok(copyButton)

    await act(async () => copyButton.click())

    assert.deepEqual(copiedValues, [item.url])

    await act(async () => root.unmount())
    container.remove()
  })
})
