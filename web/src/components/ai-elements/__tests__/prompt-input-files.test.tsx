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

const domWindow = new Window({ url: 'https://tokeness.test/playground' })
for (const key of [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLFormElement',
  'Node',
  'Element',
  'Event',
  'File',
  'MutationObserver',
] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { PromptInput, PromptInputTextarea } = await import('../prompt-input')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} } },
})

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

describe('PromptInput caller-managed files', () => {
  after(() => domWindow.close())

  test('routes pasted and dropped files to the external attachment store', async () => {
    const files: File[] = []
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PromptInput
            accept='image/*'
            onFilesAdded={(nextFiles) => files.push(...nextFiles)}
            onSubmit={() => undefined}
          >
            <PromptInputTextarea />
          </PromptInput>
        </I18nextProvider>
      )
    })

    const pastedFile = new File(['paste'], 'paste.png', { type: 'image/png' })
    const pasteEvent = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(pasteEvent, 'clipboardData', {
      value: {
        items: [{ kind: 'file', getAsFile: () => pastedFile }],
      },
    })
    await act(async () => {
      container.querySelector('textarea')?.dispatchEvent(pasteEvent)
    })

    const droppedFile = new File(['drop'], 'drop.png', { type: 'image/png' })
    const dropEvent = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(dropEvent, 'dataTransfer', {
      value: { types: ['Files'], files: [droppedFile] },
    })
    await act(async () => {
      container.querySelector('form')?.dispatchEvent(dropEvent)
    })

    assert.deepEqual(
      files.map((file) => file.name),
      ['paste.png', 'drop.png']
    )
    await act(async () => root.unmount())
    container.remove()
  })
})
