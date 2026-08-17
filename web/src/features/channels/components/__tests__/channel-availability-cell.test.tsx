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
for (const key of [
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
  'MouseEvent',
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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { ChannelAvailabilityCell } = await import('../channel-availability-cell')
const { ChannelsProvider, useChannels } = await import('../channels-provider')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        'Channel observability': 'Channel observability',
        'First token': 'First token',
      },
    },
  },
})

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const channel = {
  id: 93,
  name: 'OpenCode Go',
  status: 1,
  group: '',
} as Channel

function ContextProbe() {
  const channels = useChannels()
  return (
    <output aria-label='channels dialog state'>
      {`${channels.open ?? 'closed'}:${channels.currentRow?.id ?? 'none'}`}
    </output>
  )
}

describe('channel availability cell interaction', () => {
  afterEach(() => {
    document.body.replaceChildren()
    window.localStorage.clear()
  })

  after(() => domWindow.close())

  test('clicking a status segment opens channel observability', async () => {
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
              <ChannelAvailabilityCell
                channel={channel}
                series={{
                  93: [
                    {
                      bucketStart: 100,
                      bucketEnd: 200,
                      requestCount: 2,
                      successCount: 2,
                      failureCount: 0,
                      successRate: 100,
                      ttftMs: 250,
                      ttftSampleCount: 2,
                      latencyMs: 250,
                    },
                  ],
                }}
              />
              <ContextProbe />
            </ChannelsProvider>
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    const statusSegment = [
      ...container.querySelectorAll<HTMLElement>('[aria-label]'),
    ].find((element) => element.getAttribute('aria-label')?.endsWith('100.0%'))
    assert.ok(statusSegment)

    await act(async () => statusSegment.click())
    assert.equal(
      container.querySelector('[aria-label="channels dialog state"]')
        ?.textContent,
      'observability:93'
    )

    await act(async () => root.unmount())
    queryClient.clear()
  })
})
