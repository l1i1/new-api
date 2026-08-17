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
  /** Average time to first downstream token. */
  ttftMs: number
  /** Compatibility alias for consumers built against the first contract. */
  latencyMs: number
}

export type ChannelAvailabilitySeries = Record<
  number,
  ChannelAvailabilityPoint[]
>

function metricCount(value: number | undefined, fallback = 0): number {
  return Number.isFinite(value) ? Math.max(0, Number(value)) : fallback
}

function weightedAverage(
  leftValue: number,
  leftWeight: number,
  rightValue: number,
  rightWeight: number
): number {
  const weight = leftWeight + rightWeight
  return weight > 0
    ? (leftValue * leftWeight + rightValue * rightWeight) / weight
    : 0
}

function rateCount(rate: number, count: number): number {
  return Math.min(count, Math.max(0, Math.round((rate * count) / 100)))
}

/**
 * Merge backend rows into one row per requested model for the channel detail
 * view. The backend keeps credential/upstream dimensions for key diagnostics;
 * the model dialog should not expose those implementation dimensions as
 * duplicate model IDs.
 */
export function dedupeChannelObservabilityRows(
  rows: ChannelObservabilityResult[]
): ChannelObservabilityResult[] {
  const merged = new Map<string, ChannelObservabilityResult>()

  for (const row of rows) {
    const key = row.requested_model
    const existing = merged.get(key)
    if (!existing) {
      merged.set(key, {
        ...row,
        credential_id: 0,
        upstream_models: [
          ...new Set([
            ...(row.upstream_models ?? []),
            ...(row.upstream_model ? [row.upstream_model] : []),
          ]),
        ],
      })
      continue
    }

    const requestCount = metricCount(existing.request_count)
    const nextRequestCount = metricCount(row.request_count)
    const attemptCount = metricCount(existing.attempt_count)
    const nextAttemptCount = metricCount(row.attempt_count)
    const existingSuccessCount = Number.isFinite(existing.request_success_count)
      ? Math.min(requestCount, metricCount(existing.request_success_count))
      : rateCount(existing.request_success_rate, requestCount)
    const nextSuccessCount = Number.isFinite(row.request_success_count)
      ? Math.min(nextRequestCount, metricCount(row.request_success_count))
      : rateCount(row.request_success_rate, nextRequestCount)
    const existingAttemptSuccessCount = rateCount(
      existing.attempt_success_rate,
      attemptCount
    )
    const nextAttemptSuccessCount = rateCount(
      row.attempt_success_rate,
      nextAttemptCount
    )
    const existingSampleCount =
      (requestCount * Math.max(0, existing.sample_coverage)) / 100
    const nextSampleCount =
      (nextRequestCount * Math.max(0, row.sample_coverage)) / 100
    const existingUsageCount =
      (requestCount * Math.max(0, existing.usage_coverage)) / 100
    const nextUsageCount =
      (nextRequestCount * Math.max(0, row.usage_coverage)) / 100

    existing.request_count = requestCount + nextRequestCount
    existing.request_success_count = existingSuccessCount + nextSuccessCount
    existing.request_failure_count = Math.max(
      0,
      existing.request_count - existing.request_success_count
    )
    existing.attempt_count = attemptCount + nextAttemptCount
    existing.request_success_rate =
      existing.request_count > 0
        ? (existing.request_success_count / existing.request_count) * 100
        : 0
    existing.attempt_success_rate =
      existing.attempt_count > 0
        ? ((existingAttemptSuccessCount + nextAttemptSuccessCount) /
            existing.attempt_count) *
          100
        : 0
    existing.cache_hit_rate = weightedAverage(
      existing.cache_hit_rate,
      existingUsageCount,
      row.cache_hit_rate,
      nextUsageCount
    )
    existing.cache_token_rate = weightedAverage(
      existing.cache_token_rate,
      requestCount,
      row.cache_token_rate,
      nextRequestCount
    )
    existing.avg_latency_ms = Math.round(
      weightedAverage(
        existing.avg_latency_ms,
        attemptCount,
        row.avg_latency_ms,
        nextAttemptCount
      )
    )
    existing.avg_request_latency_ms = Math.round(
      weightedAverage(
        existing.avg_request_latency_ms,
        requestCount,
        row.avg_request_latency_ms,
        nextRequestCount
      )
    )
    existing.avg_ttft_ms = Math.round(
      weightedAverage(
        existing.avg_ttft_ms,
        requestCount,
        row.avg_ttft_ms,
        nextRequestCount
      )
    )
    // Percentiles cannot be exactly merged from row-level percentiles. The
    // maximum is conservative and avoids understating a slower credential.
    existing.p95_latency_ms = Math.max(
      existing.p95_latency_ms,
      row.p95_latency_ms
    )
    existing.p95_request_latency_ms = Math.max(
      existing.p95_request_latency_ms,
      row.p95_request_latency_ms
    )
    existing.p95_ttft_ms = Math.max(existing.p95_ttft_ms, row.p95_ttft_ms)
    existing.p95_upstream_frt_ms = Math.max(
      existing.p95_upstream_frt_ms,
      row.p95_upstream_frt_ms
    )
    existing.sample_coverage =
      existing.request_count > 0
        ? ((existingSampleCount + nextSampleCount) / existing.request_count) *
          100
        : 0
    existing.usage_coverage =
      existing.request_count > 0
        ? ((existingUsageCount + nextUsageCount) / existing.request_count) * 100
        : 0
    existing.sample_sufficient = existingSampleCount + nextSampleCount >= 20
    existing.usage_sufficient = existingUsageCount + nextUsageCount >= 20
    existing.upstream_models = [
      ...new Set([
        ...(existing.upstream_models ?? []),
        ...(existing.upstream_model ? [existing.upstream_model] : []),
        ...(row.upstream_models ?? []),
        ...(row.upstream_model ? [row.upstream_model] : []),
      ]),
    ]
    existing.upstream_model = existing.upstream_models[0]
    existing.credential_id = 0
    existing.group = existing.group === row.group ? existing.group : ''
    existing.protocol =
      existing.protocol === row.protocol ? existing.protocol : ''
    const previousErrorTrends = existing.error_trends ?? {}
    const errorClasses = new Set([
      ...Object.keys(previousErrorTrends),
      ...Object.keys(row.error_trends ?? {}),
    ])
    existing.error_trends = {}
    for (const errorClass of errorClasses) {
      existing.error_trends[errorClass] =
        (previousErrorTrends[errorClass] ?? 0) +
        (row.error_trends?.[errorClass] ?? 0)
    }
  }

  return [...merged.values()]
}

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
  let ttftTotal = 0

  for (const row of rows) {
    const count = Math.max(0, Number(row.request_count) || 0)
    requestCount += count
    successCount += getSuccessfulRequests(row)
    const hasTtft = Object.hasOwn(row, 'avg_ttft_ms')
    const ttftMs = hasTtft ? row.avg_ttft_ms : row.avg_latency_ms
    if (count > 0 && Number.isFinite(ttftMs)) {
      ttftTotal += count * (ttftMs ?? 0)
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
    ttftMs: requestCount > 0 ? Math.round(ttftTotal / requestCount) : 0,
    latencyMs: requestCount > 0 ? Math.round(ttftTotal / requestCount) : 0,
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
        ttftMs: point.avg_ttft_ms ?? point.avg_latency_ms,
        latencyMs: point.avg_ttft_ms ?? point.avg_latency_ms,
      }))
    }
  }

  return series
}
