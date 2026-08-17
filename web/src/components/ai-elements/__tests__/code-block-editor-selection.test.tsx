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

import { EditorView } from '@codemirror/view'
import { Window } from 'happy-dom'

const domWindow = new Window({ url: 'https://tokeness.test/playground' })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act, useState } = await import('react')
const { createRoot } = await import('react-dom/client')
const { CodeBlockEditor } = await import('../code-block')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function ControlledEditor() {
  const [value, setValue] = useState('alpha beta')

  return (
    <CodeBlockEditor
      ariaLabel='Message editor'
      language='markdown'
      onChange={setValue}
      onKeyDown={() => undefined}
      value={value}
    />
  )
}

after(() => {
  domWindow.close()
})

describe('CodeBlockEditor selection', () => {
  test('keeps the editor instance and caret after controlled input updates', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => root.render(<ControlledEditor />))

    const content = container.querySelector<HTMLElement>('.cm-content')
    assert.ok(content)
    const initialView = EditorView.findFromDOM(content)
    assert.ok(initialView)
    initialView.dispatch({ selection: { anchor: 6 } })

    await act(async () => {
      initialView.dispatch({
        changes: { from: 6, insert: 'X' },
        selection: { anchor: 7 },
      })
    })

    const updatedContent = container.querySelector<HTMLElement>('.cm-content')
    assert.ok(updatedContent)
    const updatedView = EditorView.findFromDOM(updatedContent)
    assert.ok(updatedView)
    assert.equal(updatedView, initialView)
    assert.equal(updatedView.state.doc.toString(), 'alpha Xbeta')
    assert.equal(updatedView.state.selection.main.head, 7)

    await act(async () => root.unmount())
    container.remove()
  })
})
