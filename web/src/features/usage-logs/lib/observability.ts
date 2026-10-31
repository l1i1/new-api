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
/**
 * Display model for the observability fields of a usage log: the serving node,
 * the fit-capability pin, relay payload sizes and transparent continuation.
 *
 * Everything here is pure so the parsing/formatting rules stay unit-testable;
 * labels are resolved through the caller's `t` so the model is locale-aware
 * without importing i18n state.
 */
import type {
  LogBodyBytes,
  LogContinuation,
  LogFitInfo,
  LogOtherData,
} from '../types'

export type ObservabilityTranslate = (key: string) => string

export interface ObservabilityDetailRow {
  label: string
  value: string
}

export interface ObservabilityFit {
  family?: string
  marks: string[]
  rules: string[]
  /** Policy version/hash the pin was measured against (bottom-of-popover mono footer). */
  policy?: string
  baseline?: string
  /** Fit Family / Fit Behaviours / Fit Rules rows, only for the values present. */
  rows: ObservabilityDetailRow[]
}

export interface ObservabilityPayload {
  request?: number
  upstreamRequest?: number
  response?: number
  /**
   * Compact `in → out` table cell text, e.g. `1.2 KB → 55.6 KB`. Missing sides
   * are shown as an em dash; undefined when neither side was measured.
   */
  compact?: string
  /** Request Body / Upstream Request Body / Response Body rows, present-only. */
  rows: ObservabilityDetailRow[]
}

export interface ObservabilityContinuation {
  rounds: number
  modes: string[]
  finish?: string
  maxSteps?: number
  extraCompletionTokens?: number
  /** Badge text, e.g. `Continued ×2`. */
  badge: string
  /** Rounds / Mode / Finish / Extra tokens rows for the badge tooltip. */
  tooltipRows: ObservabilityDetailRow[]
  /** Rounds / mode ids / Finish / Continuation tokens rows for the details dialog. */
  detailRows: ObservabilityDetailRow[]
}

export interface LogObservability {
  nodeName?: string
  fit?: ObservabilityFit
  payload?: ObservabilityPayload
  continuation?: ObservabilityContinuation
}

const MISSING_BYTES = '—'

function positiveFinite(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
    ? value
    : undefined
}

function trimmed(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text === '' ? undefined : text
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value
    .map((item) => trimmed(item))
    .filter((item): item is string => item != null)
}

/** Group digits with commas without depending on the runtime locale. */
export function formatCount(value: number): string {
  return String(Math.round(value)).replaceAll(/\B(?=(\d{3})+(?!\d))/g, ',')
}

function trimFraction(text: string): string {
  return text.includes('.') ? text.replace(/\.?0+$/, '') : text
}

/**
 * Compact, locale-independent byte size. Sizes are binary (1 KB = 1024 B);
 * `undefined` means "not measured", which is different from `0 B`.
 */
export function formatBytes(bytes?: number | null): string | undefined {
  if (bytes == null || !Number.isFinite(bytes)) return undefined
  const normalized = Math.round(bytes)
  if (normalized === 0) return '0 B'
  const sign = normalized < 0 ? '-' : ''
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let index = 0
  let value = Math.abs(normalized)
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024
    index += 1
  }
  if (index === 0) return `${sign}${value} B`
  let text = trimFraction(value.toFixed(1))
  // 1048575 B rounds to "1024 KB"; promote it so the unit never overflows.
  if (Number(text) >= 1024 && index < units.length - 1) {
    value /= 1024
    index += 1
    text = trimFraction(value.toFixed(1))
  }
  return `${sign}${text} ${units[index]}`
}

/** Serving node name; whitespace-only values count as absent. */
export function buildObservabilityNode(
  other: LogOtherData | null | undefined
): string | undefined {
  return trimmed(other?.admin_info?.node_name)
}

export function buildObservabilityFit(
  fit: LogFitInfo | null | undefined,
  t: ObservabilityTranslate
): ObservabilityFit | undefined {
  if (!fit) return undefined
  const family = trimmed(fit.family)
  const marks = stringList(fit.marks)
  const rules = stringList(fit.rules)
  if (!family && marks.length === 0 && rules.length === 0) return undefined
  const policy =
    fit.policy == null || fit.policy === ''
      ? undefined
      : String(fit.policy).trim() || undefined
  const rows: ObservabilityDetailRow[] = []
  if (family) rows.push({ label: t('Fit Family'), value: family })
  if (marks.length > 0) {
    rows.push({ label: t('Fit Behaviours'), value: marks.join(', ') })
  }
  if (rules.length > 0) {
    rows.push({ label: t('Fit Rules'), value: rules.join(', ') })
  }
  return {
    family,
    marks,
    rules,
    policy,
    baseline: trimmed(fit.baseline),
    rows,
  }
}

export function buildObservabilityPayload(
  bytes: LogBodyBytes | null | undefined,
  t: ObservabilityTranslate
): ObservabilityPayload | undefined {
  if (!bytes) return undefined
  const request = positiveFinite(bytes.request)
  const upstreamRequest = positiveFinite(bytes.upstream_request)
  const response = positiveFinite(bytes.response)
  if (request == null && upstreamRequest == null && response == null) {
    return undefined
  }

  const rows: ObservabilityDetailRow[] = []
  if (request != null) {
    rows.push({
      label: t('Request Body'),
      value: formatBytes(request) ?? MISSING_BYTES,
    })
  }
  if (upstreamRequest != null) {
    rows.push({
      label: t('Upstream Request Body'),
      value: formatBytes(upstreamRequest) ?? MISSING_BYTES,
    })
  }
  if (response != null) {
    rows.push({
      label: t('Response Body'),
      value: formatBytes(response) ?? MISSING_BYTES,
    })
  }

  const requestText = formatBytes(request) ?? MISSING_BYTES
  const responseText = formatBytes(response) ?? MISSING_BYTES
  return {
    request,
    upstreamRequest,
    response,
    compact: `${requestText} → ${responseText}`,
    rows,
  }
}

export function buildObservabilityContinuation(
  continuation: LogContinuation | null | undefined,
  t: ObservabilityTranslate
): ObservabilityContinuation | undefined {
  const rounds = positiveFinite(continuation?.rounds)
  if (rounds == null) return undefined

  const modes = stringList(continuation?.modes)
  const finish = trimmed(continuation?.finish)
  const maxSteps = positiveFinite(continuation?.max_steps)
  const extraCompletionTokens = positiveFinite(
    continuation?.extra_completion_tokens
  )

  const tooltipRows: ObservabilityDetailRow[] = []
  tooltipRows.push({
    label: t('Continuation Rounds'),
    value: formatCount(rounds),
  })
  if (modes.length > 0) {
    tooltipRows.push({
      label: t('Continuation Mode'),
      value: modes.join(', '),
    })
  }
  if (finish) {
    tooltipRows.push({ label: t('Finish'), value: finish })
  }
  if (extraCompletionTokens != null) {
    tooltipRows.push({
      label: t('Extra Completion Tokens'),
      value: formatCount(extraCompletionTokens),
    })
  }

  const detailRows: ObservabilityDetailRow[] = []
  detailRows.push({
    label: t('Continuation Rounds'),
    value: formatCount(rounds),
  })
  if (modes.length > 0) {
    detailRows.push({ label: t('Continuation'), value: modes.join(', ') })
  }
  if (finish) {
    detailRows.push({ label: t('Finish'), value: finish })
  }
  if (extraCompletionTokens != null) {
    detailRows.push({
      label: t('Continuation Tokens'),
      value: formatCount(extraCompletionTokens),
    })
  }

  return {
    rounds,
    modes,
    finish,
    maxSteps,
    extraCompletionTokens,
    badge: `${t('Continued')} ×${rounds}`,
    tooltipRows,
    detailRows,
  }
}

/**
 * Assemble everything the usage-log surfaces need for one log. Returns
 * undefined when the log carries none of the observability fields (older logs,
 * non-relay logs, or a non-admin response with admin_info stripped).
 */
export function buildLogObservability(
  other: LogOtherData | null | undefined,
  t: ObservabilityTranslate
): LogObservability | undefined {
  const nodeName = buildObservabilityNode(other)
  const fit = buildObservabilityFit(other?.fit, t)
  const payload = buildObservabilityPayload(other?.admin_info?.body_bytes, t)
  const continuation = buildObservabilityContinuation(
    other?.admin_info?.continuation,
    t
  )
  if (!nodeName && !fit && !payload && !continuation) return undefined
  return { nodeName, fit, payload, continuation }
}
