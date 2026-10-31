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
import { render, screen, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SystemInstancesPanel } from '../components/system-instances-panel'

function instance(
  nodeName: string,
  extra?: { traffic?: Record<string, number> }
) {
  return {
    node_name: nodeName,
    status: 'online' as const,
    stale_after_seconds: 90,
    started_at: 1_700_000_000,
    last_seen_at: 1_700_000_100,
    info: {
      node: { name: nodeName },
      host: { hostname: `${nodeName}.internal` },
      role: { is_master: false },
      runtime: { version: '0.8.1', goos: 'linux', goarch: 'amd64' },
      resources: { cpu: { usage_percent: 12 }, memory: { usage_percent: 34 } },
      ...(extra ? { extra } : {}),
    },
  }
}

async function renderPanel(instances: ReturnType<typeof instance>[]) {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, message: '', data: instances },
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SystemInstancesPanel />
    </QueryClientProvider>
  )
  return screen.findByText(instances[0].node_name)
}

afterEach(() => {
  vi.restoreAllMocks()
})

it('renders RPM, TPM, Traffic and Bandwidth columns between Storage and Version', async () => {
  await renderPanel([instance('edge-a')])
  expect(
    screen.getAllByRole('columnheader').map((cell) => cell.textContent)
  ).toEqual([
    'Instances',
    'Status',
    'Role',
    'CPU',
    'Memory',
    'Storage',
    'RPM',
    'TPM',
    'Traffic',
    'Bandwidth',
    'Version',
    'Runtime',
    'Started',
    'Last Seen',
    'Actions',
  ])
  expect(
    screen.getByText(
      "Requests and tokens are rolling 60-second values reported by each node's heartbeat."
    )
  ).toBeVisible()
})

it('renders heartbeat traffic values and dashes when traffic is absent', async () => {
  await renderPanel([
    instance('edge-a', {
      traffic: {
        window_seconds: 60,
        rpm: 12,
        tpm: 4600,
        prompt_tpm: 1200,
        completion_tpm: 3400,
        request_bytes: 123456,
        response_bytes: 4567890,
      },
    }),
    instance('edge-b'),
  ])

  const busyRow = screen.getByText('edge-a').closest('tr')
  expect(busyRow).not.toBeNull()
  if (!busyRow) return
  expect(within(busyRow).getByText('12')).toBeVisible()
  expect(within(busyRow).getByText('4.6k')).toBeVisible()
  expect(within(busyRow).getByText(/120\.6 KB/)).toBeVisible()
  expect(within(busyRow).getByText(/4\.4 MB/)).toBeVisible()
  // 123456 B / 60 s ≈ 2 KB/s, 4567890 B / 60 s ≈ 74.3 KB/s
  expect(within(busyRow).getByText(/2 KB\/s/)).toBeVisible()
  expect(within(busyRow).getByText(/74\.3 KB\/s/)).toBeVisible()

  const idleRow = screen.getByText('edge-b').closest('tr')
  expect(idleRow).not.toBeNull()
  if (!idleRow) return
  expect(within(idleRow).getAllByText('-').length).toBeGreaterThanOrEqual(3)
  expect(within(idleRow).queryByText('↓')).toBeNull()
})
