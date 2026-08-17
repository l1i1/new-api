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
import {
  MULTI_KEY_STATUS_CONFIG,
  MULTI_KEY_CONFIRM_MESSAGES,
} from '../constants'
import type {
  ChannelObservabilityResult,
  KeyStatus,
  MultiKeyConfirmAction,
  MultiKeyTestResult,
} from '../types'

const MINIMUM_RELIABLE_SAMPLES = 20

/** Aggregate 24-hour model rows into one diagnostic summary per credential. */
export function aggregateMultiKeyObservability(
  items: ChannelObservabilityResult[]
): Record<number, ChannelObservabilityResult> {
  const metrics: Record<number, ChannelObservabilityResult> = {}

  for (const item of items) {
    if (item.credential_id <= 0) continue

    const existing = metrics[item.credential_id]
    if (!existing) {
      metrics[item.credential_id] = { ...item }
      continue
    }

    const existingRequestCount = Math.max(0, existing.request_count)
    const itemRequestCount = Math.max(0, item.request_count)
    const requestCount = existingRequestCount + itemRequestCount
    const weighted = (left: number, right: number) =>
      requestCount > 0
        ? (left * existingRequestCount + right * itemRequestCount) /
          requestCount
        : 0
    const sampleCount =
      (existingRequestCount * Math.max(0, existing.sample_coverage)) / 100 +
      (itemRequestCount * Math.max(0, item.sample_coverage)) / 100
    const usageCount =
      (existingRequestCount * Math.max(0, existing.usage_coverage)) / 100 +
      (itemRequestCount * Math.max(0, item.usage_coverage)) / 100

    metrics[item.credential_id] = {
      ...existing,
      request_count: requestCount,
      attempt_count: existing.attempt_count + item.attempt_count,
      request_success_rate: weighted(
        existing.request_success_rate,
        item.request_success_rate
      ),
      attempt_success_rate: weighted(
        existing.attempt_success_rate,
        item.attempt_success_rate
      ),
      cache_hit_rate: weighted(existing.cache_hit_rate, item.cache_hit_rate),
      p95_latency_ms: Math.max(existing.p95_latency_ms, item.p95_latency_ms),
      p95_ttft_ms: Math.max(existing.p95_ttft_ms, item.p95_ttft_ms),
      sample_coverage:
        requestCount > 0 ? (sampleCount / requestCount) * 100 : 0,
      usage_coverage: requestCount > 0 ? (usageCount / requestCount) * 100 : 0,
      sample_sufficient: sampleCount >= MINIMUM_RELIABLE_SAMPLES,
      usage_sufficient: usageCount >= MINIMUM_RELIABLE_SAMPLES,
    }
  }

  return metrics
}

export function getMultiKeyIndex(
  key: Pick<KeyStatus, 'index' | 'position'>,
  fallback = 0
): number {
  const index = Number(key.index)
  if (Number.isInteger(index) && index >= 0) return index

  const position = Number(key.position)
  if (Number.isInteger(position) && position >= 0) return position

  return fallback
}

export function getMultiKeyTestResult(
  testResults: Record<number, MultiKeyTestResult>,
  key: KeyStatus
): MultiKeyTestResult | undefined {
  // Stable credential IDs must never silently resolve to a different legacy row.
  if (key.credential_id !== undefined && key.credential_id !== null) {
    const credentialId = Number(key.credential_id)
    if (!Number.isInteger(credentialId) || credentialId <= 0) return undefined
    return testResults[credentialId]
  }

  return testResults[getMultiKeyIndex(key)]
}

export function formatMultiKeyTestResult(
  result: MultiKeyTestResult | undefined,
  key: KeyStatus | undefined,
  translate: (value: string) => string = (value) => value
): string | null {
  const status = result?.status ?? key?.last_test_status
  if (!status) return null
  if (status !== 'failed') return translate(status)

  const httpStatus = result?.http_status ?? key?.last_test_http_status
  const error =
    result?.error_message ??
    key?.last_test_error_message ??
    result?.error_code ??
    key?.last_test_error_code ??
    result?.error_class ??
    key?.last_test_error_class

  return [
    translate('Failed'),
    httpStatus ? `${translate('HTTP status')}: ${httpStatus}` : null,
    error,
  ]
    .filter(Boolean)
    .join(' · ')
}

/**
 * Get status badge configuration for multi-key status
 */
export function getMultiKeyStatusConfig(status: number) {
  return (
    MULTI_KEY_STATUS_CONFIG[status as keyof typeof MULTI_KEY_STATUS_CONFIG] || {
      variant: 'neutral' as const,
      label: 'Unknown',
    }
  )
}

/**
 * Get confirmation message for multi-key action
 */
export function getMultiKeyConfirmMessage(
  action: MultiKeyConfirmAction | null
): string {
  if (!action) return ''

  switch (action.type) {
    case 'delete':
      return MULTI_KEY_CONFIRM_MESSAGES.DELETE
    case 'enable':
      return MULTI_KEY_CONFIRM_MESSAGES.ENABLE
    case 'disable':
      return MULTI_KEY_CONFIRM_MESSAGES.DISABLE
    case 'enable-all':
      return MULTI_KEY_CONFIRM_MESSAGES.ENABLE_ALL
    case 'disable-all':
      return MULTI_KEY_CONFIRM_MESSAGES.DISABLE_ALL
    case 'enable-selected':
      return MULTI_KEY_CONFIRM_MESSAGES.ENABLE_ALL
    case 'disable-selected':
      return MULTI_KEY_CONFIRM_MESSAGES.DISABLE_ALL
    case 'delete-disabled':
      return MULTI_KEY_CONFIRM_MESSAGES.DELETE_DISABLED
    default:
      return ''
  }
}

/**
 * Check if action is destructive
 */
export function isDestructiveAction(
  action: MultiKeyConfirmAction | null
): boolean {
  if (!action) return false
  return (
    action.type === 'delete' ||
    action.type === 'delete-disabled' ||
    action.type === 'disable-all' ||
    action.type === 'disable-selected'
  )
}
