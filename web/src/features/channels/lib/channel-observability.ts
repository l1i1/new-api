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
import { getChannelAvailability } from '../api'
import type { ChannelObservabilityResult } from '../types'

export const CHANNEL_AVAILABILITY_BUCKETS = 24
export const CHANNEL_AVAILABILITY_MAX_IDS = 200

export type ChannelAvailabilityFetcher = typeof getChannelAvailability

export interface ChannelAvailabilityPoint {
  bucketStart: number
  bucketEnd: number
  requestCount: number
  successCount: number
  failureCount: number
  successRate: number
  latencyMs: number
}

export type ChannelAvailabilitySeries = Record<
  number,
  ChannelAvailabilityPoint[]
>

export function collectChannelAvailabilityIds(
  rows: Array<{
    id: number
    children?: Array<{ id: number }>
  }>
): number[] {
  const ids = rows.flatMap((row) =>
    row.children ? row.children.map((child) => child.id) : [row.id]
  )
  return [...new Set(ids)].filter((id) => Number.isInteger(id) && id > 0)
}

export function chunkChannelAvailabilityIds(
  channelIds: number[],
  chunkSize = CHANNEL_AVAILABILITY_MAX_IDS
): number[][] {
  if (!Number.isInteger(chunkSize) || chunkSize <= 0) {
    throw new Error('chunkSize must be a positive integer')
  }

  const ids = [...new Set(channelIds)].filter(
    (id) => Number.isInteger(id) && id > 0
  )
  const chunks: number[][] = []
  for (let index = 0; index < ids.length; index += chunkSize) {
    chunks.push(ids.slice(index, index + chunkSize))
  }
  return chunks
}

function getSuccessfulRequests(row: ChannelObservabilityResult): number {
  const count = Math.max(0, Number(row.request_count) || 0)
  if (Number.isFinite(row.request_success_count)) {
    return Math.min(count, Math.max(0, Number(row.request_success_count) || 0))
  }
  const rate = Math.min(100, Math.max(0, Number(row.request_success_rate) || 0))
  return Math.min(count, Math.max(0, Math.round((count * rate) / 100)))
}

export function aggregateChannelAvailability(
  rows: ChannelObservabilityResult[],
  bucketStart: number,
  bucketEnd: number
): ChannelAvailabilityPoint {
  let requestCount = 0
  let successCount = 0
  let latencyTotal = 0

  for (const row of rows) {
    const count = Math.max(0, Number(row.request_count) || 0)
    requestCount += count
    successCount += getSuccessfulRequests(row)
    if (count > 0 && Number.isFinite(row.avg_latency_ms)) {
      latencyTotal += count * row.avg_latency_ms
    }
  }

  const failureCount = Math.max(0, requestCount - successCount)
  return {
    bucketStart,
    bucketEnd,
    requestCount,
    successCount,
    failureCount,
    successRate: requestCount > 0 ? (successCount / requestCount) * 100 : 0,
    latencyMs: requestCount > 0 ? Math.round(latencyTotal / requestCount) : 0,
  }
}

export async function getChannelAvailabilitySeries(
  channelIds: number[],
  hours = 24,
  bucketCount = CHANNEL_AVAILABILITY_BUCKETS,
  fetchAvailability: ChannelAvailabilityFetcher = getChannelAvailability
): Promise<ChannelAvailabilitySeries> {
  const chunks = chunkChannelAvailabilityIds(channelIds)
  const ids = chunks.flat()
  const series: ChannelAvailabilitySeries = Object.fromEntries(
    ids.map((id) => [id, []])
  )
  if (ids.length === 0) return series

  for (const chunk of chunks) {
    let response: Awaited<ReturnType<ChannelAvailabilityFetcher>>
    try {
      response = await fetchAvailability(chunk, hours, bucketCount)
    } catch {
      // One failed batch should not hide availability from healthy batches.
      continue
    }
    if (!response.success) continue

    for (const item of response.data?.items ?? []) {
      if (!series[item.channel_id]) continue
      series[item.channel_id] = item.points.map((point) => ({
        bucketStart: point.bucket_start,
        bucketEnd: point.bucket_end,
        requestCount: point.request_count,
        successCount: point.request_success_count,
        failureCount: point.request_failure_count,
        successRate: point.request_success_rate,
        latencyMs: point.avg_latency_ms,
      }))
    }
  }

  return series
}
