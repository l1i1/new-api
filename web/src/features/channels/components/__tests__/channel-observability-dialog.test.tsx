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

import type { Channel } from '../../types'

const domWindow = new Window({ url: 'https://tokeness.test/channels' })
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
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
] as const) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act, useEffect } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { api } = await import('@/lib/api')
const { ChannelsProvider, useChannels } = await import('../channels-provider')
const { ChannelObservabilityDialog } =
  await import('../dialogs/channel-observability-dialog')
const { MultiKeyManageDialog } =
  await import('../dialogs/multi-key-manage-dialog')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function DialogHarness() {
  const { setCurrentRow } = useChannels()

  useEffect(() => {
    setCurrentRow({ id: 93 } as Channel)
  }, [setCurrentRow])

  return <ChannelObservabilityDialog open onOpenChange={() => undefined} />
}

function MultiKeyDialogHarness() {
  const { setCurrentRow } = useChannels()

  useEffect(() => {
    setCurrentRow({
      id: 94,
      name: 'Multi-key channel',
      channel_info: { multi_key_mode: 'random' },
    } as Channel)
  }, [setCurrentRow])

  return <MultiKeyManageDialog open onOpenChange={() => undefined} />
}

describe('channel observability dialog layout', () => {
  const originalApiGet = api.get

  afterEach(() => {
    api.get = originalApiGet
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('overrides the default desktop dialog width', async () => {
    api.get = (async () => ({
      data: {
        success: true,
        data: { items: [], page: 1, page_size: 200, total: 0, total_pages: 1 },
      },
    })) as typeof api.get

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <ChannelsProvider>
              <DialogHarness />
            </ChannelsProvider>
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    const content = document.querySelector<HTMLElement>(
      '[data-slot="dialog-content"]'
    )
    assert.ok(content)
    assert.equal(
      content.classList.contains('sm:max-w-[min(96vw,1440px)]'),
      true
    )
    assert.equal(content.classList.contains('sm:max-w-2xl'), false)

    await act(async () => root.unmount())
    queryClient.clear()
  })

  test('gives multi-key management the same responsive desktop width', async () => {
    api.get = (async (url: string) => {
      if (url.includes('/multi-key')) {
        return {
          data: {
            success: true,
            data: {
              keys: [],
              total: 0,
              page: 1,
              page_size: 10,
              total_pages: 0,
              enabled_count: 0,
              manual_disabled_count: 0,
              auto_disabled_count: 0,
            },
          },
        }
      }
      return {
        data: {
          success: true,
          data: {
            items: [],
            page: 1,
            page_size: 200,
            total: 0,
            total_pages: 1,
          },
        },
      }
    }) as typeof api.get

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <ChannelsProvider>
              <MultiKeyDialogHarness />
            </ChannelsProvider>
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    const content = document.querySelector<HTMLElement>(
      '[data-slot="dialog-content"]'
    )
    assert.ok(content)
    assert.equal(
      content.classList.contains('sm:max-w-[min(96vw,1440px)]'),
      true
    )
    assert.equal(content.classList.contains('sm:max-w-2xl'), false)

    await act(async () => root.unmount())
    queryClient.clear()
  })
})
