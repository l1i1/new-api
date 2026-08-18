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

const domWindow = new Window({ url: 'https://tokeness.test/channels' })
for (const key of [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLInputElement',
  'HTMLTextAreaElement',
  'Node',
  'Element',
  'Event',
  'MutationObserver',
  'getComputedStyle',
] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { ChannelKeyRevealField } = await import('../channel-key-reveal-field')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('ChannelKeyRevealField', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('uses a multiline read-only field for a multi-key reveal', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <ChannelKeyRevealField
          isMultiKey
          value={'key-one\nhttp://proxy.example:8080\nkey-two'}
          placeholder='Hidden'
        />
      )
    })

    const textarea = container.querySelector('textarea')
    assert.ok(textarea)
    assert.equal(textarea.readOnly, true)
    assert.equal(textarea.value, 'key-one\nhttp://proxy.example:8080\nkey-two')
    assert.equal(container.querySelector('input'), null)

    await act(async () => root.unmount())
  })

  test('keeps a compact input for a single-key reveal', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <ChannelKeyRevealField
          isMultiKey={false}
          value='single-key'
          placeholder='Hidden'
        />
      )
    })

    const input = container.querySelector('input')
    assert.ok(input)
    assert.equal(input.readOnly, true)
    assert.equal(input.value, 'single-key')
    assert.equal(container.querySelector('textarea'), null)

    await act(async () => root.unmount())
  })
})
