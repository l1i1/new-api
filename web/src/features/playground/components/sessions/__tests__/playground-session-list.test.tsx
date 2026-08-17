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

import type { PlaygroundSession } from '../../../types'

const domWindow = new Window({ url: 'https://tokeness.test/playground' })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
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
const { PlaygroundSessionList } = await import('../playground-session-list')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function session(id: string, title: string): PlaygroundSession {
  return {
    id,
    title,
    createdAt: 1,
    updatedAt: 1,
    mode: 'chat',
    config: {
      model: '',
      group: '',
      temperature: 1,
      top_p: 1,
      max_tokens: 2048,
      frequency_penalty: 0,
      presence_penalty: 0,
      seed: null,
      stream: true,
    },
    parameterEnabled: {
      temperature: true,
      top_p: true,
      max_tokens: true,
      frequency_penalty: false,
      presence_penalty: false,
      seed: false,
    },
    messages: [],
  }
}

describe('PlaygroundSessionList keyboard reordering', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('moves a focused chat one position with the arrow keys', async () => {
    const calls: Array<[string, string, 'before' | 'after' | undefined]> = []
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PlaygroundSessionList
            activeSessionId='second'
            onCreateSession={() => undefined}
            onDeleteSession={() => undefined}
            onRenameSession={() => undefined}
            onReorderSessions={(sourceId, targetId, placement) =>
              calls.push([sourceId, targetId, placement])
            }
            onSelectSession={() => undefined}
            sessions={[session('first', 'First'), session('second', 'Second')]}
          />
        </I18nextProvider>
      )
    })

    const handles = container.querySelectorAll<HTMLButtonElement>(
      'button[aria-keyshortcuts="ArrowUp ArrowDown"]'
    )
    assert.equal(handles.length, 2)

    await act(async () => {
      handles[1]?.dispatchEvent(
        new KeyboardEvent('keydown', {
          bubbles: true,
          key: 'ArrowUp',
        })
      )
    })

    assert.deepEqual(calls, [['second', 'first', 'before']])
    await act(async () => root.unmount())
  })

  test('moves a focused chat down after the next row', async () => {
    const calls: Array<[string, string, 'before' | 'after' | undefined]> = []
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PlaygroundSessionList
            activeSessionId='second'
            onCreateSession={() => undefined}
            onDeleteSession={() => undefined}
            onRenameSession={() => undefined}
            onReorderSessions={(sourceId, targetId, placement) =>
              calls.push([sourceId, targetId, placement])
            }
            onSelectSession={() => undefined}
            sessions={[
              session('first', 'First'),
              session('second', 'Second'),
              session('third', 'Third'),
            ]}
          />
        </I18nextProvider>
      )
    })

    const handles = container.querySelectorAll<HTMLButtonElement>(
      'button[aria-keyshortcuts="ArrowUp ArrowDown"]'
    )
    await act(async () => {
      handles[1]?.dispatchEvent(
        new KeyboardEvent('keydown', {
          bubbles: true,
          key: 'ArrowDown',
        })
      )
    })

    assert.deepEqual(calls, [['second', 'third', 'after']])
    await act(async () => root.unmount())
  })

  test('sets a native drag payload and move effect', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PlaygroundSessionList
            activeSessionId='first'
            onCreateSession={() => undefined}
            onDeleteSession={() => undefined}
            onRenameSession={() => undefined}
            onReorderSessions={() => undefined}
            onSelectSession={() => undefined}
            sessions={[session('first', 'First'), session('second', 'Second')]}
          />
        </I18nextProvider>
      )
    })

    const transfer = {
      effectAllowed: '',
      setDataCalls: [] as Array<[string, string]>,
      setData(type: string, value: string) {
        this.setDataCalls.push([type, value])
      },
    }
    const dragStart = new Event('dragstart', { bubbles: true })
    Object.defineProperty(dragStart, 'dataTransfer', { value: transfer })

    const firstRow = container.querySelector('li')
    assert.ok(firstRow)
    await act(async () => firstRow.dispatchEvent(dragStart))

    assert.equal(transfer.effectAllowed, 'move')
    assert.deepEqual(transfer.setDataCalls, [['text/plain', 'first']])
    await act(async () => root.unmount())
  })

  test('places a dropped chat before or after the target midpoint', async () => {
    const calls: Array<[string, string, 'before' | 'after' | undefined]> = []
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PlaygroundSessionList
            activeSessionId='first'
            onCreateSession={() => undefined}
            onDeleteSession={() => undefined}
            onRenameSession={() => undefined}
            onReorderSessions={(sourceId, targetId, placement) =>
              calls.push([sourceId, targetId, placement])
            }
            onSelectSession={() => undefined}
            sessions={[
              session('first', 'First'),
              session('second', 'Second'),
              session('third', 'Third'),
            ]}
          />
        </I18nextProvider>
      )
    })

    const rows = container.querySelectorAll<HTMLLIElement>('li')
    const targetRow = rows[1]
    assert.ok(targetRow)
    Object.defineProperty(targetRow, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ top: 100, height: 40 }),
    })

    const makeDropEvent = (clientY: number) => {
      const event = new Event('drop', { bubbles: true, cancelable: true })
      Object.defineProperty(event, 'clientY', { value: clientY })
      Object.defineProperty(event, 'dataTransfer', {
        value: { getData: () => 'first' },
      })
      return event
    }

    await act(async () => targetRow.dispatchEvent(makeDropEvent(105)))
    await act(async () => targetRow.dispatchEvent(makeDropEvent(135)))

    assert.deepEqual(calls, [
      ['first', 'second', 'before'],
      ['first', 'second', 'after'],
    ])
    await act(async () => root.unmount())
  })

  test('supports dropping after the last row', async () => {
    const calls: Array<[string, string, 'before' | 'after' | undefined]> = []
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PlaygroundSessionList
            activeSessionId='first'
            onCreateSession={() => undefined}
            onDeleteSession={() => undefined}
            onRenameSession={() => undefined}
            onReorderSessions={(sourceId, targetId, placement) =>
              calls.push([sourceId, targetId, placement])
            }
            onSelectSession={() => undefined}
            sessions={[
              session('first', 'First'),
              session('second', 'Second'),
              session('third', 'Third'),
            ]}
          />
        </I18nextProvider>
      )
    })

    const lastRow = container.querySelectorAll<HTMLLIElement>('li')[2]
    assert.ok(lastRow)
    Object.defineProperty(lastRow, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ top: 100, height: 40 }),
    })
    const drop = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(drop, 'clientY', { value: 139 })
    Object.defineProperty(drop, 'dataTransfer', {
      value: { getData: () => 'first' },
    })

    await act(async () => lastRow.dispatchEvent(drop))

    assert.deepEqual(calls, [['first', 'third', 'after']])
    await act(async () => root.unmount())
  })
})
