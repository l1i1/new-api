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
import { describe, test } from 'node:test'

import { api } from '@/lib/api'

import { getAllChannelObservability } from '../../api'

describe('channel observability pagination', () => {
  test('loads every page instead of truncating at 200 rows', async () => {
    const originalGet = api.get
    const requestedPages: number[] = []

    api.get = (async (_url, config) => {
      const page = Number(
        (config as { params?: { page?: number } } | undefined)?.params?.page
      )
      requestedPages.push(page)
      return {
        data: {
          success: true,
          data: {
            items: [
              {
                channel_id: 7,
                credential_id: page,
                requested_model: `model-${page}`,
                group: 'default',
                protocol: 'openai',
                request_count: page,
                attempt_count: page,
                request_success_rate: 100,
                attempt_success_rate: 100,
                cache_hit_rate: 0,
                cache_token_rate: 0,
                avg_latency_ms: 0,
                p95_latency_ms: 0,
                avg_request_latency_ms: 0,
                p95_request_latency_ms: 0,
                avg_ttft_ms: 0,
                p95_ttft_ms: 0,
                avg_upstream_frt_ms: 0,
                p95_upstream_frt_ms: 0,
                sample_coverage: 100,
                usage_coverage: 0,
                sample_sufficient: true,
                usage_sufficient: false,
              },
            ],
            total: 2,
            page,
            page_size: 200,
            total_pages: 2,
          },
        },
      }
    }) as typeof api.get

    try {
      const rows = await getAllChannelObservability(7)
      assert.deepEqual(requestedPages, [1, 2])
      assert.deepEqual(
        rows.map((row) => row.requested_model),
        ['model-1', 'model-2']
      )
    } finally {
      api.get = originalGet
    }
  })
})
