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

import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { Window } from 'happy-dom'

import type { Channel } from '../../types'

const domWindow = new Window({ url: 'https://tokeness.test/channels' })
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
  'localStorage',
  'customElements',
  'HTMLElement',
  'HTMLAnchorElement',
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
  'ShadowRoot',
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
const matchMedia = () => ({
  matches: false,
  addEventListener: () => undefined,
  removeEventListener: () => undefined,
})
Object.defineProperty(domWindow, 'matchMedia', {
  configurable: true,
  value: matchMedia,
})
Object.defineProperty(globalThis, 'matchMedia', {
  configurable: true,
  value: matchMedia,
})

const { act, useEffect } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { ChannelsProvider, useChannels } = await import('../channels-provider')
const { useChannelsColumns } = await import('../channels-columns')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} } },
})

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createChannel(baseUrl: string | null): Channel {
  return {
    id: 93,
    type: 999,
    key: '',
    openai_organization: null,
    test_model: null,
    status: 1,
    name: 'OpenCode Go',
    weight: 1,
    created_time: 1,
    test_time: 0,
    response_time: 0,
    base_url: baseUrl,
    other: '',
    balance: 0,
    balance_updated_time: 0,
    models: 'test-model',
    group: 'default',
    used_quota: 0,
    model_mapping: null,
    status_code_mapping: null,
    priority: 0,
    auto_ban: 1,
    other_info: '',
    tag: null,
    setting: null,
    param_override: null,
    header_override: null,
    remark: '',
    max_input_tokens: 0,
    channel_info: {
      is_multi_key: false,
      multi_key_size: 0,
      multi_key_polling_index: 0,
      multi_key_mode: 'random',
    },
    settings: '{}',
  }
}

function NameCellFixture(props: {
  channel: Channel
  sensitiveVisible: boolean
}) {
  const { setSensitiveVisible } = useChannels()
  useEffect(() => {
    setSensitiveVisible(props.sensitiveVisible)
  }, [props.sensitiveVisible, setSensitiveVisible])

  const columns = useChannelsColumns({ enableSelection: false })
  const table = useReactTable({
    data: [props.channel],
    columns,
    getCoreRowModel: getCoreRowModel(),
  })
  const nameCell = table
    .getRowModel()
    .rows[0]?.getAllCells()
    .find((cell) => cell.column.id === 'name')

  if (!nameCell?.column.columnDef.cell) return null
  return flexRender(nameCell.column.columnDef.cell, nameCell.getContext())
}

async function renderNameCell(channel: Channel, sensitiveVisible = true) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <ChannelsProvider>
            <NameCellFixture
              channel={channel}
              sensitiveVisible={sensitiveVisible}
            />
          </ChannelsProvider>
        </I18nextProvider>
      </QueryClientProvider>
    )
  })

  return { container, queryClient, root }
}

describe('channel name API link', () => {
  afterEach(() => {
    document.body.replaceChildren()
    window.localStorage.clear()
  })

  after(() => domWindow.close())

  test('opens a valid channel API address in a new tab', async () => {
    const rendered = await renderNameCell(
      createChannel(' https://api.example.com/v1 ')
    )
    const link = rendered.container.querySelector<HTMLAnchorElement>('a')

    assert.ok(link)
    assert.equal(link.getAttribute('href'), 'https://api.example.com/v1')
    assert.equal(link.target, '_blank')
    assert.equal(link.rel, 'noopener noreferrer')
    assert.equal(link.textContent, 'OpenCode Go')

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })

  test('keeps missing and unsafe API addresses as plain text', async () => {
    for (const baseUrl of [null, 'javascript:alert(1)']) {
      const rendered = await renderNameCell(createChannel(baseUrl))

      assert.equal(rendered.container.querySelector('a'), null)
      assert.equal(rendered.container.textContent, 'OpenCode Go')

      await act(async () => rendered.root.unmount())
      rendered.queryClient.clear()
    }
  })

  test('does not expose the API address when sensitive details are hidden', async () => {
    const rendered = await renderNameCell(
      createChannel('https://api.example.com/v1'),
      false
    )

    assert.equal(rendered.container.querySelector('a'), null)
    assert.equal(rendered.container.textContent, '••••')

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })
})
