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
import type { TrafficSnapshot } from '../types'

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const
const COUNT_UNITS: ReadonlyArray<readonly [number, string]> = [
  [1_000_000_000, 'B'],
  [1_000_000, 'M'],
  [1_000, 'k'],
]

/**
 * Coerce a heartbeat counter into a non-negative integer.
 *
 * Heartbeat JSON is untrusted by construction (older nodes omit fields, a broken
 * node may send null/strings), so anything that is not a finite, non-negative
 * number is reported as "unknown" instead of being rendered as 0 or NaN.
 */
export function toCount(value: unknown): number | null {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) {
    return null
  }
  return Math.trunc(value)
}

function trimTrailingZero(text: string): string {
  return text.endsWith('.0') ? text.slice(0, -2) : text
}

/** Grouped integer, `-` when the value is unknown. */
export function formatInteger(
  value: number | null | undefined,
  locales?: Intl.LocalesArgument
): string {
  const count = toCount(value)
  if (count === null) return '-'
  return new Intl.NumberFormat(locales, {
    maximumFractionDigits: 0,
  }).format(count)
}

/**
 * Compact count for table cells: `999`, `1k`, `1.2k`, `12.5k`, `3.4M`.
 * Deterministic (no locale-dependent suffix) and `-` when unknown/negative.
 */
export function formatCompactNumber(value: number | null | undefined): string {
  const count = toCount(value)
  if (count === null) return '-'
  if (count < 1000) return String(count)

  const index = COUNT_UNITS.findIndex(([divisor]) => count >= divisor)
  if (index === -1) return String(count)

  let [divisor, suffix] = COUNT_UNITS[index]
  let scaled = count / divisor
  // 999_999k would be nonsense: promote to the next unit instead (→ 1M).
  if (scaled >= 999.95 && index > 0) {
    const [nextDivisor, nextSuffix] = COUNT_UNITS[index - 1]
    divisor = nextDivisor
    suffix = nextSuffix
    scaled = count / divisor
  }

  const digits = scaled < 100 ? 1 : 0
  return `${trimTrailingZero(scaled.toFixed(digits))}${suffix}`
}

/** Human-readable byte size (1024-based): `0 B`, `1.5 KB`, `4.4 MB`. */
export function formatBytes(
  value: number | null | undefined,
  locales?: Intl.LocalesArgument
): string {
  const bytes = toCount(value)
  if (bytes === null) return '-'
  if (bytes === 0) return '0 B'

  const index = Math.min(
    Math.floor(Math.log(bytes) / Math.log(1024)),
    BYTE_UNITS.length - 1
  )
  if (index === 0) return `${formatInteger(bytes, locales)} B`

  const scaled = bytes / 1024 ** index
  return `${new Intl.NumberFormat(locales, {
    maximumFractionDigits: 1,
  }).format(scaled)} ${BYTE_UNITS[index]}`
}

/** Exact byte count for tooltips: `4,567,890 B`. */
export function formatExactBytes(
  value: number | null | undefined,
  locales?: Intl.LocalesArgument
): string {
  const bytes = toCount(value)
  if (bytes === null) return '-'
  return `${formatInteger(bytes, locales)} B`
}

/**
 * Average bytes per second over an explicit statistics window.
 * `null` when the byte counter or the window is unknown/zero: a rate without a
 * window cannot be derived, and guessing 60 s would silently mislabel a
 * differently-sized window (the heartbeat is the only source of that fact).
 */
export function toBandwidth(
  bytes: number | null,
  windowSeconds: number | null
): number | null {
  if (bytes === null || windowSeconds === null || windowSeconds <= 0) {
    return null
  }
  return bytes / windowSeconds
}

/**
 * Human-readable average bandwidth (1024-based, per second) for a table line:
 * `0 B/s`, `3 B/s`, `25.6 KB/s`, `4.4 MB/s`. `-` when unknown.
 */
export function formatBandwidth(
  value: number | null | undefined,
  locales?: Intl.LocalesArgument
): string {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) {
    return '-'
  }
  if (value === 0) return '0 B/s'

  const index = Math.min(
    Math.floor(Math.log(value) / Math.log(1024)),
    BYTE_UNITS.length - 1
  )
  const scaled = value / 1024 ** index
  const digits = index === 0 && scaled >= 10 ? 0 : 1
  const text = new Intl.NumberFormat(locales, {
    maximumFractionDigits: digits,
  }).format(scaled)
  return `${trimTrailingZero(text)} ${BYTE_UNITS[index]}/s`
}

/**
 * Exact bandwidth for tooltips: `1,234 B/s (9,872 bps)`.
 * Bits per second are shown because "bandwidth" is usually quoted in bits,
 * while the heartbeat counts payload bytes.
 */
export function formatExactBandwidth(
  value: number | null | undefined,
  locales?: Intl.LocalesArgument
): string {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) {
    return '-'
  }
  const bytesPerSecond = Math.round(value)
  return `${formatInteger(bytesPerSecond, locales)} B/s (${formatInteger(
    bytesPerSecond * 8,
    locales
  )} bps)`
}

export type TrafficCellModel = {
  /** True when the heartbeat actually carried traffic counters (even if zero). */
  hasTraffic: boolean
  rpmValue: number | null
  rpm: string
  tpmValue: number | null
  tpm: string
  promptTokensValue: number | null
  completionTokensValue: number | null
  requestBytesValue: number | null
  responseBytesValue: number | null
  /** Process-lifetime totals behind the rolling window (reset on node restart). */
  requestsTotalValue: number | null
  requestsTotal: string
  tokensTotalValue: number | null
  tokensTotal: string
  requestBytesTotalValue: number | null
  requestBytesTotal: string
  responseBytesTotalValue: number | null
  responseBytesTotal: string
  inbound: string
  outbound: string
  /** Average inbound/outbound bandwidth over the stats window (bytes/s). */
  inboundBandwidthValue: number | null
  outboundBandwidthValue: number | null
  inboundBandwidth: string
  outboundBandwidth: string
  windowSeconds: number | null
}

const ABSENT_TRAFFIC_CELL: TrafficCellModel = {
  hasTraffic: false,
  rpmValue: null,
  rpm: '-',
  tpmValue: null,
  tpm: '-',
  promptTokensValue: null,
  completionTokensValue: null,
  requestBytesValue: null,
  responseBytesValue: null,
  requestsTotalValue: null,
  requestsTotal: '-',
  tokensTotalValue: null,
  tokensTotal: '-',
  requestBytesTotalValue: null,
  requestBytesTotal: '-',
  responseBytesTotalValue: null,
  responseBytesTotal: '-',
  inbound: '-',
  outbound: '-',
  inboundBandwidthValue: null,
  outboundBandwidthValue: null,
  inboundBandwidth: '-',
  outboundBandwidth: '-',
  windowSeconds: null,
}

/**
 * Build the three instances-table cells (RPM / TPM / Traffic) from one node's
 * `info.extra.traffic`. Absent or malformed traffic yields `hasTraffic: false`
 * and `-` everywhere; a legitimately idle node (all counters zero) yields `0`.
 */
export function buildTrafficCell(
  traffic: Partial<TrafficSnapshot> | null | undefined,
  locales?: Intl.LocalesArgument
): TrafficCellModel {
  if (!traffic || typeof traffic !== 'object') return ABSENT_TRAFFIC_CELL

  const rpm = toCount(traffic.rpm)
  const tpm = toCount(traffic.tpm)
  const promptTokens = toCount(traffic.prompt_tpm)
  const completionTokens = toCount(traffic.completion_tpm)
  const requestBytes = toCount(traffic.request_bytes)
  const responseBytes = toCount(traffic.response_bytes)
  const requestsTotal = toCount(traffic.requests_total)
  const tokensTotal = toCount(traffic.tokens_total)
  const requestBytesTotal = toCount(traffic.request_bytes_total)
  const responseBytesTotal = toCount(traffic.response_bytes_total)

  const windowSeconds = toCount(traffic.window_seconds)
  const inboundBandwidth = toBandwidth(requestBytes, windowSeconds)
  const outboundBandwidth = toBandwidth(responseBytes, windowSeconds)

  const hasTraffic = [
    rpm,
    tpm,
    promptTokens,
    completionTokens,
    requestBytes,
    responseBytes,
  ].some((value) => value !== null)
  if (!hasTraffic) return ABSENT_TRAFFIC_CELL

  return {
    hasTraffic: true,
    rpmValue: rpm,
    rpm: formatInteger(rpm, locales),
    tpmValue: tpm,
    tpm: formatCompactNumber(tpm),
    promptTokensValue: promptTokens,
    completionTokensValue: completionTokens,
    requestBytesValue: requestBytes,
    responseBytesValue: responseBytes,
    requestsTotalValue: requestsTotal,
    requestsTotal: formatInteger(requestsTotal, locales),
    tokensTotalValue: tokensTotal,
    tokensTotal: formatCompactNumber(tokensTotal),
    requestBytesTotalValue: requestBytesTotal,
    requestBytesTotal: formatBytes(requestBytesTotal, locales),
    responseBytesTotalValue: responseBytesTotal,
    responseBytesTotal: formatBytes(responseBytesTotal, locales),
    inbound: formatBytes(requestBytes, locales),
    outbound: formatBytes(responseBytes, locales),
    inboundBandwidthValue: inboundBandwidth,
    outboundBandwidthValue: outboundBandwidth,
    inboundBandwidth: formatBandwidth(inboundBandwidth, locales),
    outboundBandwidth: formatBandwidth(outboundBandwidth, locales),
    windowSeconds,
  }
}
