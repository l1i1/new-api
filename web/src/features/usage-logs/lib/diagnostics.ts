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
 * Display models for the admin-only diagnostic fields of a usage log that are
 * not part of the relay observability block: stream-cut diagnostics, the
 * billing-audit markers, conversion diagnostics, override audits and the
 * request-capture sampling marker.
 *
 * Everything here is pure so the label/value rules stay unit-testable; labels
 * are resolved through the caller's `t`.
 */
import type {
  LogBillingMarker,
  LogConversionDiagnostic,
  LogOtherData,
  LogRequestCapture,
  LogStreamCut,
} from '../types'
import { formatCount } from './observability'

export type DiagnosticTranslate = (key: string) => string

export interface DiagnosticRow {
  label: string
  value: string
}

export interface DiagnosticSection {
  label: string
  variant?: 'default' | 'danger'
  rows: DiagnosticRow[]
}

function hasText(value: string | undefined): value is string {
  return typeof value === 'string' && value.trim() !== ''
}

function numberOrNull(value: number | undefined): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

/**
 * Why a stream ended without a usable terminal result, and what the repair
 * path had to work with. Presence of the section means the request failed on a
 * cut; every number is what the gateway observed before giving up.
 */
export function buildStreamCutSection(
  cut: LogStreamCut | null | undefined,
  t: DiagnosticTranslate
): DiagnosticSection | undefined {
  if (!cut) return undefined

  const rows: DiagnosticRow[] = []
  let shape = t('Unknown')
  if (hasText(cut.finish_reason)) {
    shape = `finish_reason=${cut.finish_reason}`
  } else if (cut.missing_finish) {
    shape = t('Missing finish')
  }
  rows.push({ label: t('Cut shape'), value: shape })
  if (cut.reasoning_truncated) {
    rows.push({ label: t('Reasoning truncated'), value: t('Yes') })
  }
  rows.push({
    label: t('Visible content chars'),
    value: formatCount(numberOrNull(cut.visible_content_chars) ?? 0),
  })
  rows.push({
    label: t('Reasoning chars'),
    value: formatCount(numberOrNull(cut.reasoning_chars) ?? 0),
  })
  const toolCalls = numberOrNull(cut.tool_calls)
  if (toolCalls != null && toolCalls > 0) {
    rows.push({ label: t('Tool calls'), value: formatCount(toolCalls) })
  }
  const choices = numberOrNull(cut.choice_count)
  if (choices != null && choices > 1) {
    rows.push({ label: t('Choices'), value: formatCount(choices) })
  }

  const budgetParts: string[] = []
  const completion = numberOrNull(cut.completion_tokens)
  const maxTokens = numberOrNull(cut.client_max_tokens)
  const budgetLeft = numberOrNull(cut.budget_left)
  if (completion != null) {
    budgetParts.push(`${t('Completion')} ${formatCount(completion)}`)
  }
  if (maxTokens != null) {
    budgetParts.push(`${t('Max')} ${formatCount(maxTokens)}`)
  }
  if (budgetLeft != null) {
    budgetParts.push(`${t('Left')} ${formatCount(budgetLeft)}`)
  }
  if (budgetParts.length > 0) {
    rows.push({ label: t('Budget'), value: budgetParts.join(' · ') })
  }

  const configured: string[] = []
  if (cut.continuation_configured) configured.push('length')
  if (cut.abnormal_configured) configured.push('abnormal')
  if (cut.missing_configured) configured.push('missing')
  rows.push({
    label: t('Continuation configured'),
    value: configured.length > 0 ? configured.join(', ') : t('None'),
  })
  if (cut.thinking_to_content) {
    rows.push({
      label: t('Thinking to content'),
      value: t('Enabled'),
    })
  }
  if (cut.reasoning_suppressed) {
    rows.push({ label: t('Reasoning suppressed'), value: t('Yes') })
  }

  if (cut.repair_attempted) {
    const repair: string[] = []
    if (hasText(cut.repair_mode)) repair.push(cut.repair_mode)
    const rounds = numberOrNull(cut.repair_rounds)
    if (rounds != null && rounds > 0) repair.push(`${rounds}×`)
    if (hasText(cut.repair_failed_reason)) {
      repair.push(cut.repair_failed_reason)
    }
    rows.push({ label: t('Repair'), value: repair.join(' · ') || t('Yes') })
    const channels = Array.isArray(cut.repair_channels)
      ? cut.repair_channels.filter((id) => Number.isFinite(id))
      : []
    if (channels.length > 0) {
      rows.push({
        label: t('Repair channels'),
        value: channels.map(String).join(' → '),
      })
    }
  } else {
    rows.push({ label: t('Repair'), value: t('Not attempted') })
  }

  return { label: t('Stream cut diagnostics'), variant: 'danger', rows }
}

function buildBillingMarkerRows(
  marker: LogBillingMarker,
  t: DiagnosticTranslate
): DiagnosticRow[] {
  const rows: DiagnosticRow[] = []
  if (hasText(marker.kind)) {
    rows.push({ label: t('Kind'), value: marker.kind })
  }
  if (hasText(marker.field)) {
    rows.push({ label: t('Field'), value: marker.field })
  }
  const value = numberOrNull(marker.value)
  if (value != null) {
    rows.push({ label: t('Value'), value: formatCount(value) })
  }
  const threshold = numberOrNull(marker.threshold)
  if (threshold != null) {
    rows.push({ label: t('Threshold'), value: formatCount(threshold) })
  }
  if (hasText(marker.action)) {
    rows.push({ label: t('Action'), value: marker.action })
  }
  if (hasText(marker.reason)) {
    rows.push({ label: t('Reason'), value: marker.reason })
  }
  const upstreamPrompt = numberOrNull(marker.upstream_prompt_tokens)
  if (upstreamPrompt != null) {
    rows.push({
      label: t('Upstream prompt tokens'),
      value: formatCount(upstreamPrompt),
    })
  }
  const upstreamCompletion = numberOrNull(marker.upstream_completion_tokens)
  if (upstreamCompletion != null) {
    rows.push({
      label: t('Upstream completion tokens'),
      value: formatCount(upstreamCompletion),
    })
  }
  if (marker.raw && typeof marker.raw === 'object') {
    const raw = Object.entries(marker.raw)
      .filter(([, fieldValue]) => Number.isFinite(fieldValue))
      .map(([key, fieldValue]) => `${key}=${fieldValue}`)
    if (raw.length > 0) {
      rows.push({ label: t('Raw usage'), value: raw.join(' ') })
    }
  }
  return rows
}

/**
 * Billing-audit markers. `billing_anomaly`/`undelivered` mean the charge was
 * zeroed for a documented reason; `usage_deviation` is alarm-only. They are
 * separate sections so an operator scanning a row can tell which one fired.
 */
export function buildBillingAuditSections(
  adminInfo: NonNullable<LogOtherData['admin_info']> | null | undefined,
  t: DiagnosticTranslate
): DiagnosticSection[] {
  if (!adminInfo) return []
  const sections: DiagnosticSection[] = []
  const entries: Array<[keyof typeof adminInfo, string, 'default' | 'danger']> =
    [
      ['billing_anomaly', t('Billing anomaly'), 'danger'],
      ['undelivered', t('Undelivered response'), 'danger'],
      ['usage_deviation', t('Usage deviation'), 'default'],
    ]
  for (const [field, label, variant] of entries) {
    const marker = adminInfo[field] as LogBillingMarker | undefined
    if (!marker || typeof marker !== 'object') continue
    const rows = buildBillingMarkerRows(marker, t)
    if (rows.length === 0) continue
    sections.push({ label, variant, rows })
  }
  return sections
}

/** Lossy conversion details: what the request/response format conversion dropped. */
export function buildConversionSection(
  diagnostics: LogConversionDiagnostic[] | null | undefined,
  truncated: boolean | undefined,
  t: DiagnosticTranslate
): DiagnosticSection | undefined {
  const rows: DiagnosticRow[] = []
  if (Array.isArray(diagnostics)) {
    for (const item of diagnostics) {
      if (!item || typeof item !== 'object') continue
      const parts: string[] = []
      if (hasText(item.severity)) parts.push(item.severity.toUpperCase())
      if (hasText(item.from) && hasText(item.to)) {
        parts.push(`${item.from}→${item.to}`)
      }
      if (hasText(item.path)) parts.push(item.path)
      if (hasText(item.code)) parts.push(item.code)
      rows.push({
        label: t('Conversion diagnostics'),
        value: parts.join(' · '),
      })
      if (hasText(item.message)) {
        rows.push({ label: t('Reason'), value: item.message })
      }
    }
  }
  if (truncated) {
    rows.push({
      label: t('Conversion diagnostics truncated'),
      value: t('Yes'),
    })
  }
  if (rows.length === 0) return undefined
  return { label: t('Conversion diagnostics'), rows }
}

/** Param-override / response-override audit lines ("po"/"ro"). */
export function buildOverridesSection(
  other: LogOtherData | null | undefined,
  t: DiagnosticTranslate
): DiagnosticSection | undefined {
  const rows: DiagnosticRow[] = []
  for (const [field, label] of [
    ['po', t('Param overrides')],
    ['ro', t('Response overrides')],
  ] as const) {
    const lines = other?.[field]
    if (!Array.isArray(lines)) continue
    for (const line of lines) {
      if (hasText(line)) rows.push({ label, value: line })
    }
  }
  if (rows.length === 0) return undefined
  return { label: t('Overrides'), rows }
}

/** Reasoning observability: the caller's thinking intent and what came back. */
export function buildReasoningSection(
  other: LogOtherData | null | undefined,
  t: DiagnosticTranslate
): DiagnosticSection | undefined {
  if (!other) return undefined
  const rows: DiagnosticRow[] = []
  if (hasText(other.thinking_type)) {
    rows.push({ label: t('Thinking type'), value: other.thinking_type })
  }
  if (other.reasoning_content_seen) {
    rows.push({ label: t('Reasoning content seen'), value: t('Yes') })
  }
  const upstreamReasoning = numberOrNull(other.upstream_reasoning_tokens)
  if (upstreamReasoning != null) {
    rows.push({
      label: t('Upstream reasoning tokens'),
      value: formatCount(upstreamReasoning),
    })
  }
  if (rows.length === 0) return undefined
  return { label: t('Reasoning'), rows }
}

/** request_capture sampling marker: which rule matched and where the file is. */
export function buildCaptureSection(
  capture: LogRequestCapture | null | undefined,
  t: DiagnosticTranslate
): DiagnosticSection | undefined {
  if (!capture) return undefined
  const rows: DiagnosticRow[] = []
  if (hasText(capture.rule)) {
    rows.push({ label: t('Capture rule'), value: capture.rule })
  }
  if (hasText(capture.file)) {
    rows.push({ label: t('Capture file'), value: capture.file })
  }
  if (rows.length === 0) return undefined
  return { label: t('Request capture'), rows }
}

/**
 * Stream closure details that only make sense on a completed stream: a
 * provider that sent its terminal frame and then kept the connection open, and
 * a cut tool-call stream that was closed as a complete tool_calls answer.
 */
export function buildStreamClosureRows(
  other: LogOtherData | null | undefined,
  closedAsToolCalls: boolean | undefined,
  t: DiagnosticTranslate
): DiagnosticRow[] {
  const rows: DiagnosticRow[] = []
  if (other?.stream_status?.closed_after_terminal) {
    rows.push({ label: t('Closed after terminal finish'), value: t('Yes') })
  }
  if (closedAsToolCalls) {
    rows.push({ label: t('Closed as tool calls'), value: t('Yes') })
  }
  const endReason = other?.stream_status?.end_reason
  if (hasText(endReason)) {
    rows.push({ label: t('Stream end reason'), value: endReason })
  }
  const errors = other?.stream_status?.error_count
  if (typeof errors === 'number' && errors > 0) {
    rows.push({ label: t('Soft Errors'), value: formatCount(errors) })
  }
  return rows
}
