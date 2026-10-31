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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { render, renderHook, screen } from '@testing-library/react'
import { expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { DetailsDialog } from '../dialogs/details-dialog'
import { UsageLogsProvider } from '../usage-logs-provider'

const otherWithObservability: LogOtherData = {
  admin_info: {
    node_name: 'i-2ze0abcdef',
    body_bytes: { request: 1234, upstream_request: 1250, response: 56789 },
    continuation: {
      modes: ['length'],
      rounds: 2,
      finish: 'stop',
      max_steps: 2,
      extra_completion_tokens: 4096,
    },
    stream_cut: {
      finish_reason: 'length',
      visible_content_chars: 0,
      reasoning_chars: 120,
      client_max_tokens: 64000,
      completion_tokens: 300,
      budget_left: 63700,
      continuation_configured: true,
      repair_attempted: true,
      repair_mode: 'abnormal',
      repair_rounds: 2,
      repair_failed_reason: 'round_incomplete',
      repair_channels: [106, 105],
    },
    closed_as_tool_calls: true,
    billing_model: 'kimi-k3-billing',
    last_upstream_error: 'upstream: stream ended unexpectedly',
    capture: {
      rule: 'kimi-200-sample',
      file: '2026-10-31/120000-kimi-200-sample-req-1.json',
    },
  },
  po: ['reasoning_effort: set max'],
  ro: ['choices[0].message.content: rewritten'],
  thinking_type: 'disabled',
  upstream_reasoning_tokens: 1024,
  stream_status: {
    closed_after_terminal: true,
    end_reason: 'terminal_tail_grace',
  },
  fit: {
    family: 'kimi-k3',
    marks: ['tools', 'history'],
    rules: ['r12'],
    policy: 0,
    baseline: 'a5615b8de20971fd',
  },
}

function makeLog(other: LogOtherData): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    username: 'user',
    token_name: 'token',
    model_name: 'kimi-k3',
    quota: 5000,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 1,
    is_stream: true,
    channel: 1,
    channel_name: '',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

/** Renders only the requested column cells for one log row. */
function Cells(props: {
  other: LogOtherData
  isAdmin: boolean
  ids: string[]
}) {
  const columns = useCommonLogsColumns(props.isAdmin, false)
  const table = useReactTable({
    data: [makeLog(props.other)],
    columns,
    getCoreRowModel: getCoreRowModel(),
  })
  const row = table.getRowModel().rows[0]
  // The Details cell reads billing/status data through react-query.
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return (
    <QueryClientProvider client={queryClient}>
      <UsageLogsProvider>
        {props.ids.map((id) => {
          const cell = row.getAllCells().find((item) => item.column.id === id)
          if (!cell) return null
          return (
            <div key={id}>
              {flexRender(cell.column.columnDef.cell, cell.getContext())}
            </div>
          )
        })}
      </UsageLogsProvider>
    </QueryClientProvider>
  )
}

const observabilityCellIds = ['channel', 'content', 'is_stream']

test('the channel cell carries the fit pin; the Details cell the node and payload', () => {
  render(
    <Cells other={otherWithObservability} isAdmin ids={observabilityCellIds} />
  )

  // The fit-capability pin is a routing fact, so it sits with the channel boot;
  // the serving node and payload sizes live in the Details cell behind the
  // full breakdown in the details dialog.
  expect(screen.getByRole('button', { name: 'Fit Pin' })).toBeVisible()
  expect(screen.getByText('i-2ze0abcdef')).toBeVisible()
  expect(screen.getByText('1.2 KB → 55.5 KB')).toBeVisible()
  expect(screen.getByText('Continued ×2')).toBeVisible()
})

test('non-admin Details cells omit the relay observability facts', () => {
  render(
    <Cells other={otherWithObservability} isAdmin={false} ids={['content']} />
  )

  expect(screen.queryByText('i-2ze0abcdef')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Fit Pin' })).toBeNull()
  expect(screen.queryByText('1.2 KB → 55.5 KB')).toBeNull()
})

test('the channel and Details cells render no observability meta without data', () => {
  render(<Cells other={{}} isAdmin ids={observabilityCellIds} />)

  expect(screen.queryByText('Continued ×2')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Fit Pin' })).toBeNull()
  expect(screen.queryByText('1.2 KB → 55.5 KB')).toBeNull()
})

test('non-admin tables do not carry the admin-only observability columns', () => {
  const { result } = renderHook(() => {
    const columns = useCommonLogsColumns(false, false)
    const table = useReactTable({
      data: [],
      columns,
      getCoreRowModel: getCoreRowModel(),
    })
    return table.getAllColumns().map((column) => column.id)
  })
  expect(result.current).not.toContain('node')
  expect(result.current).not.toContain('body_bytes')
})

function renderDetails(other: LogOtherData, isAdmin: boolean): QueryClient {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(['status'], {}, { updatedAt: freshAt })
  queryClient.setQueryData(
    ['pricing'],
    { data: [], vendors: [] },
    { updatedAt: freshAt }
  )
  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={makeLog(other)}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
  return queryClient
}

function rowValue(label: string): string | null {
  return screen.getByText(label).nextElementSibling?.textContent ?? null
}

test('details dialog shows the admin observability section', () => {
  const queryClient = renderDetails(otherWithObservability, true)

  expect(screen.getByText('Observability')).toBeVisible()
  expect(rowValue('Node')).toBe('i-2ze0abcdef')
  expect(rowValue('Fit Family')).toBe('kimi-k3')
  expect(rowValue('Fit Behaviours')).toBe('tools, history')
  expect(rowValue('Fit Rules')).toBe('r12')
  expect(rowValue('Request Body')).toBe('1.2 KB')
  expect(rowValue('Upstream Request Body')).toBe('1.2 KB')
  expect(rowValue('Response Body')).toBe('55.5 KB')
  expect(rowValue('Continuation Rounds')).toBe('2')
  expect(rowValue('Max steps')).toBe('2')
  expect(rowValue('Continuation')).toBe('length')
  expect(rowValue('Continuation Tokens')).toBe('4,096')
  expect(rowValue('Closed after terminal finish')).toBe('Yes')
  expect(rowValue('Closed as tool calls')).toBe('Yes')
  expect(rowValue('Actual billing model')).toBe('kimi-k3-billing')
  expect(rowValue('Last upstream error')).toBe(
    'upstream: stream ended unexpectedly'
  )
  // Stream-cut diagnostics, capture marker, override and reasoning audits.
  expect(screen.getByText('Stream cut diagnostics')).toBeVisible()
  expect(rowValue('Cut shape')).toBe('finish_reason=length')
  expect(rowValue('Repair')).toBe('abnormal · 2× · round_incomplete')
  expect(rowValue('Repair channels')).toBe('106 → 105')
  expect(screen.getByText('Request capture')).toBeVisible()
  expect(rowValue('Capture rule')).toBe('kimi-200-sample')
  // Param overrides keep their own dedicated section; this one only adds the
  // response-side rewrite.
  expect(rowValue('Response overrides')).toBe(
    'choices[0].message.content: rewritten'
  )
  expect(screen.getByText('Reasoning')).toBeVisible()
  expect(rowValue('Thinking type')).toBe('disabled')
  expect(rowValue('Upstream reasoning tokens')).toBe('1,024')
  queryClient.clear()
})

test('details dialog hides the observability section from non-admins', () => {
  const queryClient = renderDetails(otherWithObservability, false)

  expect(screen.queryByText('Observability')).toBeNull()
  expect(screen.queryByText('Fit Family')).toBeNull()
  queryClient.clear()
})

test('details dialog omits the observability section for logs without it', () => {
  const queryClient = renderDetails({}, true)

  expect(screen.queryByText('Observability')).toBeNull()
  queryClient.clear()
})
