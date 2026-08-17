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
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from '@tanstack/react-table'
import { Window } from 'happy-dom'

import type { Channel } from '../../types'

const domWindow = new Window({ url: 'https://tokeness.test/channels' })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { ChannelCard } = await import('../channel-card')
const { ChannelsProvider } = await import('../channels-provider')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: { Availability: 'Availability' } } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const channel = {
  id: 93,
  name: 'OpenCode Go',
  status: 1,
  group: '',
} as Channel

const cell = (label: string) => () => <span>{label}</span>
const columns: ColumnDef<Channel>[] = [
  { id: 'select', cell: cell('Select') },
  { id: 'type', cell: cell('Type') },
  { id: 'name', cell: cell('Name') },
  { id: 'status', cell: cell('Status') },
  { id: 'actions', cell: cell('Actions') },
  { id: 'priority', cell: cell('Priority value') },
  { id: 'weight', cell: cell('Weight value') },
  { id: 'balance', cell: cell('Balance value') },
  { id: 'response_time', cell: cell('Response value') },
  { id: 'test_time', cell: cell('Test value') },
  {
    id: 'availability',
    cell: () => (
      <div data-testid='availability-monitor'>Availability trend</div>
    ),
  },
]

function CardFixture() {
  const table = useReactTable({
    data: [channel],
    columns,
    getCoreRowModel: getCoreRowModel(),
  })

  return <ChannelCard row={table.getRowModel().rows[0]} isSelected={false} />
}

describe('channel card availability', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('renders the availability monitor in the default card view', async () => {
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
              <CardFixture />
            </ChannelsProvider>
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    assert.ok(container.querySelector('[data-testid="availability-monitor"]'))

    await act(async () => root.unmount())
    queryClient.clear()
  })
})
