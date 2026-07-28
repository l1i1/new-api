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

import type { Channel } from '../../types'

const domWindow = new Window({ url: 'https://tokeness.test/channels' })
const domGlobals = [
  'window',
  'document',
  'navigator',
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
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { api } = await import('@/lib/api')
const { ChannelUsedQuotaResetDialog } =
  await import('../dialogs/channel-used-quota-reset-dialog')
const i18n = createInstance()
const originalApiPost = api.post
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        Cancel: 'Cancel',
        Reset: 'Reset',
        'Reset channel used quota': 'Reset channel used quota',
        'Reset used quota for channel "{{name}}"? This only clears the channel usage counter and does not affect billing logs or user balances.':
          'Reset used quota for channel "{{name}}"? This only clears the channel usage counter and does not affect billing logs or user balances.',
        'Used quota reset successfully': 'Used quota reset successfully',
        'Failed to reset used quota': 'Failed to reset used quota',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function findButton(name: string): HTMLButtonElement {
  const button = [...document.querySelectorAll('button')].find(
    (candidate) => candidate.textContent?.trim() === name
  )
  assert.ok(button)
  return button
}

async function renderDialog(options: {
  canOperate: boolean
  onOpenChange: (open: boolean) => void
}) {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const channel = { id: 93, name: 'OpenCode Go' } as Channel

  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <ChannelUsedQuotaResetDialog
            channel={channel}
            open
            onOpenChange={options.onOpenChange}
            canOperate={options.canOperate}
          />
        </I18nextProvider>
      </QueryClientProvider>
    )
  })

  return { container, queryClient, root }
}

describe('channel used quota reset dialog', () => {
  afterEach(() => {
    api.post = originalApiPost
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('requires operate permission and explicit confirmation', async () => {
    let requests = 0
    api.post = (async () => {
      requests += 1
      return { data: { success: true } }
    }) as typeof api.post
    const rendered = await renderDialog({
      canOperate: false,
      onOpenChange: () => undefined,
    })

    assert.equal(requests, 0)
    const confirmButton = findButton('Reset')
    assert.equal(confirmButton.disabled, true)
    await act(async () => confirmButton.click())
    assert.equal(requests, 0)

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })

  test('submits once, disables while pending, and closes after success', async () => {
    let resolveRequest:
      | ((value: { data: { success: boolean } }) => void)
      | null = null
    let requests = 0
    api.post = (() => {
      requests += 1
      return new Promise<{ data: { success: boolean } }>((resolve) => {
        resolveRequest = resolve
      })
    }) as typeof api.post
    const openChanges: boolean[] = []
    const rendered = await renderDialog({
      canOperate: true,
      onOpenChange: (open) => openChanges.push(open),
    })

    assert.equal(requests, 0)
    const confirmButton = findButton('Reset')
    await act(async () => confirmButton.click())
    assert.equal(requests, 1)
    assert.equal(confirmButton.disabled, true)
    await act(async () => confirmButton.click())
    assert.equal(requests, 1)

    await act(async () => resolveRequest?.({ data: { success: true } }))
    assert.deepEqual(openChanges, [false])

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })

  test('keeps the dialog open when the business response fails', async () => {
    api.post = (async () => ({
      data: { success: false, message: 'Database unavailable' },
    })) as typeof api.post
    const openChanges: boolean[] = []
    const rendered = await renderDialog({
      canOperate: true,
      onOpenChange: (open) => openChanges.push(open),
    })

    await act(async () => findButton('Reset').click())
    assert.deepEqual(openChanges, [])
    assert.ok(document.body.textContent?.includes('Reset channel used quota'))

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })

  test('keeps the dialog open when the HTTP request fails', async () => {
    api.post = (async () => {
      throw { response: { data: { message: 'Database unavailable' } } }
    }) as typeof api.post
    const openChanges: boolean[] = []
    const rendered = await renderDialog({
      canOperate: true,
      onOpenChange: (open) => openChanges.push(open),
    })

    await act(async () => findButton('Reset').click())
    assert.deepEqual(openChanges, [])
    assert.ok(document.body.textContent?.includes('Reset channel used quota'))

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })
})
