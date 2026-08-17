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

import type { Message } from '../../../types'

const domWindow = new Window({ url: 'https://tokeness.test/playground' })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLAnchorElement',
  'Node',
  'Element',
  'Event',
  'KeyboardEvent',
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

Object.defineProperty(domWindow.HTMLElement.prototype, 'getAnimations', {
  configurable: true,
  value: () => [],
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { PlaygroundMessageContent } =
  await import('../playground-message-content')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: {
    en: {
      translation: {
        Download: 'Download',
        'Attached image': 'Attached image',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function imageMessage(images: string[]): Message {
  return {
    key: 'image-result',
    from: 'assistant',
    versions: [{ id: 'version', content: '' }],
    images,
  }
}

describe('PlaygroundMessageContent image downloads', () => {
  const originalFetch = globalThis.fetch
  const originalCreateObjectURL = URL.createObjectURL
  const originalRevokeObjectURL = URL.revokeObjectURL

  afterEach(() => {
    document.body.replaceChildren()
    globalThis.fetch = originalFetch
    URL.createObjectURL = originalCreateObjectURL
    URL.revokeObjectURL = originalRevokeObjectURL
  })

  after(() => {
    domWindow.close()
  })

  test('offers a download button for data and remote image results', async () => {
    const clicked: HTMLAnchorElement[] = []
    Object.defineProperty(domWindow.HTMLAnchorElement.prototype, 'click', {
      configurable: true,
      value(this: HTMLAnchorElement) {
        clicked.push(this)
      },
    })
    globalThis.fetch = async () =>
      new Response(new Blob(['remote-image'], { type: 'image/jpeg' }), {
        status: 200,
      })
    URL.createObjectURL = () => 'blob:generated-image'
    URL.revokeObjectURL = () => undefined

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PlaygroundMessageContent
            actions={null}
            alignment='left'
            message={imageMessage([
              'data:image/png;base64,ZmFrZQ==',
              'https://cdn.example.test/generated.jpg',
            ])}
            versionContent=''
          />
        </I18nextProvider>
      )
    })

    const buttons = container.querySelectorAll<HTMLButtonElement>(
      'button[aria-label="Download"]'
    )
    assert.equal(buttons.length, 2)

    await act(async () => {
      buttons[0]?.click()
      buttons[1]?.click()
      await Promise.resolve()
    })

    assert.equal(clicked.length, 2)
    assert.equal(clicked[0]?.download, 'generated-image-1.png')
    assert.equal(clicked[0]?.href, 'data:image/png;base64,ZmFrZQ==')
    assert.equal(clicked[1]?.download, 'generated-image-2.jpg')
    assert.equal(clicked[1]?.href, 'blob:generated-image')

    await act(async () => root.unmount())
  })

  test('keeps a direct download fallback when a remote image blocks CORS', async () => {
    const clicked: HTMLAnchorElement[] = []
    Object.defineProperty(domWindow.HTMLAnchorElement.prototype, 'click', {
      configurable: true,
      value(this: HTMLAnchorElement) {
        clicked.push(this)
      },
    })
    globalThis.fetch = async () => {
      throw new Error('CORS request blocked')
    }

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PlaygroundMessageContent
            actions={null}
            alignment='left'
            message={imageMessage(['https://cdn.example.test/image'])}
            versionContent=''
          />
        </I18nextProvider>
      )
    })

    const button = container.querySelector<HTMLButtonElement>(
      'button[aria-label="Download"]'
    )
    assert.ok(button)

    await act(async () => {
      button.click()
      await Promise.resolve()
    })

    assert.equal(clicked.length, 1)
    assert.equal(clicked[0]?.download, 'generated-image-1.png')
    assert.equal(clicked[0]?.href, 'https://cdn.example.test/image')
    assert.equal(clicked[0]?.target, '_blank')

    await act(async () => root.unmount())
  })
})
