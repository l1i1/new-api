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

import type {
  ChannelAvailabilityResponse,
  ChannelObservabilityResult,
} from '../../types'
import {
  aggregateChannelAvailability,
  chunkChannelAvailabilityIds,
  collectChannelAvailabilityIds,
  dedupeChannelObservabilityRows,
  getChannelAvailabilitySeries,
} from '../channel-observability'

function metric(
  requestCount: number,
  requestSuccessRate: number,
  avgLatencyMs: number
): ChannelObservabilityResult {
  return {
    channel_id: 7,
    credential_id: 0,
    requested_model: 'gpt-test',
    group: 'default',
    protocol: 'openai',
    request_count: requestCount,
    attempt_count: requestCount,
    request_success_rate: requestSuccessRate,
    attempt_success_rate: requestSuccessRate,
    cache_hit_rate: 0,
    cache_token_rate: 0,
    avg_latency_ms: avgLatencyMs,
    p95_latency_ms: avgLatencyMs,
    avg_request_latency_ms: avgLatencyMs,
    p95_request_latency_ms: avgLatencyMs,
    avg_ttft_ms: avgLatencyMs,
    ttft_count: requestCount,
    p95_ttft_ms: 0,
    avg_upstream_frt_ms: 0,
    p95_upstream_frt_ms: 0,
    sample_coverage: 100,
    usage_coverage: 0,
    sample_sufficient: true,
    usage_sufficient: false,
  }
}

describe('channel availability aggregation', () => {
  test('collects child channel ids for tag aggregate rows', () => {
    assert.deepEqual(
      collectChannelAvailabilityIds([
        { id: 0, children: [{ id: 7 }, { id: 8 }] },
        { id: 9 },
        { id: 0, children: [{ id: 8 }, { id: 10 }] },
      ]),
      [7, 8, 9, 10]
    )
  })

  test('chunks requests at the backend limit while preserving id order', () => {
    const ids = Array.from({ length: 401 }, (_, index) => index + 1)

    assert.deepEqual(
      chunkChannelAvailabilityIds(ids).map((chunk) => chunk.length),
      [200, 200, 1]
    )
    assert.deepEqual(chunkChannelAvailabilityIds(ids).flat(), ids)
  })

  test('merges successful responses from each availability batch', async () => {
    const ids = Array.from({ length: 201 }, (_, index) => index + 1)
    const requested: number[][] = []
    const fetchAvailability = async (
      batch: number[],
      hours?: number,
      bucketCount?: number
    ): Promise<ChannelAvailabilityResponse> => {
      requested.push([...batch])
      assert.equal(hours, 12)
      assert.equal(bucketCount, 6)
      return {
        success: true,
        data: {
          items: [...batch].reverse().map((channelId) => ({
            channel_id: channelId,
            points: [
              {
                bucket_start: channelId,
                bucket_end: channelId + 1,
                request_count: channelId,
                request_success_count: channelId - 1,
                request_failure_count: 1,
                request_success_rate: ((channelId - 1) / channelId) * 100,
                avg_latency_ms: channelId * 2,
              },
            ],
          })),
          start: 0,
          end: 1,
          bucket_count: 1,
        },
      }
    }

    const series = await getChannelAvailabilitySeries(
      ids,
      12,
      6,
      fetchAvailability
    )

    assert.deepEqual(
      requested.map((batch) => batch.length),
      [200, 1]
    )
    assert.deepEqual(requested.flat(), ids)
    assert.deepEqual(series[1], [
      {
        bucketStart: 1,
        bucketEnd: 2,
        requestCount: 1,
        successCount: 0,
        failureCount: 1,
        successRate: 0,
        ttftMs: 2,
        ttftSampleCount: 1,
        latencyMs: 2,
      },
    ])
    assert.equal(series[201][0]?.requestCount, 201)
  })

  test('isolates failed batches while retaining data before and after them', async () => {
    const ids = Array.from({ length: 601 }, (_, index) => index + 1)

    const successResponse = (
      channelId: number
    ): ChannelAvailabilityResponse => ({
      success: true,
      data: {
        items: [
          {
            channel_id: channelId,
            points: [
              {
                bucket_start: channelId,
                bucket_end: channelId + 1,
                request_count: channelId,
                request_success_count: channelId,
                request_failure_count: 0,
                request_success_rate: 100,
                avg_latency_ms: channelId,
              },
            ],
          },
        ],
        start: 0,
        end: 1,
        bucket_count: 1,
      },
    })

    const series = await getChannelAvailabilitySeries(
      ids,
      24,
      24,
      async (batch) => {
        if (batch[0] === 1) return successResponse(1)
        if (batch[0] === 201) return { success: false }
        if (batch[0] === 401) throw new Error('temporary availability failure')
        return successResponse(601)
      }
    )

    assert.equal(series[1][0]?.requestCount, 1)
    assert.deepEqual(series[201], [])
    assert.deepEqual(series[401], [])
    assert.equal(series[601][0]?.requestCount, 601)
  })

  test('calculates successful and failed request counts from metric rows', () => {
    const point = aggregateChannelAvailability(
      [metric(4, 75, 100), metric(1, 0, 300)],
      100,
      200
    )

    assert.equal(point.requestCount, 5)
    assert.equal(point.successCount, 3)
    assert.equal(point.failureCount, 2)
    assert.equal(point.successRate, 60)
    assert.equal(point.ttftMs, 140)
    assert.equal(point.ttftSampleCount, 5)
    assert.equal(point.latencyMs, 140)
    assert.equal(point.bucketStart, 100)
    assert.equal(point.bucketEnd, 200)
  })

  test('returns an empty healthy-looking bucket without dividing by zero', () => {
    const point = aggregateChannelAvailability([], 100, 200)

    assert.deepEqual(point, {
      bucketStart: 100,
      bucketEnd: 200,
      requestCount: 0,
      successCount: 0,
      failureCount: 0,
      successRate: 0,
      ttftMs: 0,
      ttftSampleCount: 0,
      latencyMs: 0,
    })
  })

  test('weights first-token latency by TTFT samples instead of request count', () => {
    const mostlyNonStreaming = metric(100, 100, 100)
    mostlyNonStreaming.ttft_count = 1
    const streaming = metric(10, 100, 300)
    streaming.ttft_count = 10

    const point = aggregateChannelAvailability(
      [mostlyNonStreaming, streaming],
      100,
      200
    )

    assert.equal(point.ttftSampleCount, 11)
    assert.equal(point.ttftMs, 282)
  })

  test('prefers exact success counts over rounded percentage estimates', () => {
    const row = metric(3, 66.7, 100)
    row.request_success_count = 1
    row.request_failure_count = 2

    const point = aggregateChannelAvailability([row], 100, 200)

    assert.equal(point.successCount, 1)
    assert.equal(point.failureCount, 2)
    assert.ok(Math.abs(point.successRate - 100 / 3) < 1e-12)
  })

  test('deduplicates model rows while preserving exact request counters', () => {
    const first = metric(10, 80, 100)
    first.credential_id = 11
    first.request_success_count = 8
    first.p95_latency_ms = 1234
    first.upstream_model = 'provider-model-a'
    const second = metric(5, 40, 300)
    second.credential_id = 12
    second.request_success_count = 2
    second.p95_latency_ms = 2345
    second.upstream_model = 'provider-model-b'

    const rows = dedupeChannelObservabilityRows([first, second])

    assert.equal(rows.length, 1)
    assert.equal(rows[0]?.requested_model, 'gpt-test')
    assert.equal(rows[0]?.credential_id, 0)
    assert.equal(rows[0]?.request_count, 15)
    assert.equal(rows[0]?.request_success_count, 10)
    assert.equal(rows[0]?.request_failure_count, 5)
    assert.equal(rows[0]?.request_success_rate, (10 / 15) * 100)
    assert.equal(rows[0]?.avg_latency_ms, 167)
    assert.equal(rows[0]?.p95_latency_ms, 2345)
    assert.deepEqual(rows[0]?.upstream_models, [
      'provider-model-a',
      'provider-model-b',
    ])
  })
})
