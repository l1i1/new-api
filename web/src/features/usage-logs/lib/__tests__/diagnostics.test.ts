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
import { expect, test } from 'vitest'

import type { LogOtherData } from '../../types'
import {
  buildBillingAuditSections,
  buildCaptureSection,
  buildConversionSection,
  buildOverridesSection,
  buildReasoningSection,
  buildStreamClosureRows,
  buildStreamCutSection,
} from '../diagnostics'

// The builders only pass labels through `t`, so the key is the assertion.
const t = (key: string) => key

test('stream cut section explains an unrepaired cut', () => {
  const section = buildStreamCutSection(
    {
      finish_reason: 'length',
      visible_content_chars: 0,
      reasoning_chars: 120,
      choice_count: 1,
      client_max_tokens: 64000,
      completion_tokens: 300,
      budget_left: 63700,
      continuation_configured: true,
      abnormal_configured: true,
      missing_configured: false,
      repair_attempted: true,
      repair_mode: 'abnormal',
      repair_rounds: 2,
      repair_failed_reason: 'round_incomplete',
      repair_channels: [106, 105],
    },
    t
  )

  expect(section?.label).toBe('Stream cut diagnostics')
  expect(section?.variant).toBe('danger')
  const rows = new Map(section?.rows.map((row) => [row.label, row.value]))
  expect(rows.get('Cut shape')).toBe('finish_reason=length')
  expect(rows.get('Reasoning chars')).toBe('120')
  expect(rows.get('Budget')).toBe('Completion 300 · Max 64,000 · Left 63,700')
  expect(rows.get('Continuation configured')).toBe('length, abnormal')
  expect(rows.get('Repair')).toBe('abnormal · 2× · round_incomplete')
  expect(rows.get('Repair channels')).toBe('106 → 105')
})

test('stream cut section marks an unconfigured, unattempted cut', () => {
  const section = buildStreamCutSection(
    { missing_finish: true, continuation_configured: false },
    t
  )
  const rows = new Map(section?.rows.map((row) => [row.label, row.value]))
  expect(rows.get('Cut shape')).toBe('Missing finish')
  expect(rows.get('Continuation configured')).toBe('None')
  expect(rows.get('Repair')).toBe('Not attempted')
  expect(buildStreamCutSection(undefined, t)).toBeUndefined()
})

test('billing audit emits one section per fired marker', () => {
  const adminInfo: NonNullable<LogOtherData['admin_info']> = {
    billing_anomaly: {
      kind: 'completion_overflow',
      field: 'completion_tokens',
      value: 120000,
      threshold: 65536,
      action: 'zeroed',
      raw: { p: 8000, c: 120000 },
    },
    undelivered: {
      reason: 'client_gone_before_terminal',
      upstream_prompt_tokens: 8000,
      upstream_completion_tokens: 1200,
    },
    usage_deviation: { field: 'completion_tokens', value: 40000 },
  }
  const sections = buildBillingAuditSections(adminInfo, t)
  expect(sections.map((section) => section.label)).toEqual([
    'Billing anomaly',
    'Undelivered response',
    'Usage deviation',
  ])
  const anomaly = new Map(sections[0].rows.map((row) => [row.label, row.value]))
  expect(anomaly.get('Kind')).toBe('completion_overflow')
  expect(anomaly.get('Threshold')).toBe('65,536')
  expect(anomaly.get('Raw usage')).toBe('p=8000 c=120000')
  const undelivered = new Map(
    sections[1].rows.map((row) => [row.label, row.value])
  )
  expect(undelivered.get('Reason')).toBe('client_gone_before_terminal')
  expect(undelivered.get('Upstream prompt tokens')).toBe('8,000')
  expect(buildBillingAuditSections({}, t)).toEqual([])
})

test('conversion diagnostics list each lossy detail and the truncation flag', () => {
  const section = buildConversionSection(
    [
      {
        code: 'thinking_dropped',
        path: 'messages[0].content',
        message: 'thinking block cannot be represented',
        severity: 'warning',
        from: 'claude',
        to: 'openai',
      },
    ],
    true,
    t
  )
  expect(section?.label).toBe('Conversion diagnostics')
  const rows = new Map(section?.rows.map((row) => [row.label, row.value]))
  expect(rows.get('Conversion diagnostics')).toBe(
    'WARNING · claude→openai · messages[0].content · thinking_dropped'
  )
  expect(rows.get('Conversion diagnostics truncated')).toBe('Yes')
  expect(buildConversionSection([], false, t)).toBeUndefined()
})

test('override audit renders only ro lines (po has its own section)', () => {
  const section = buildOverridesSection(
    {
      po: ['reasoning_effort: set max'],
      ro: ['choices[0].message.content: rewritten'],
    },
    t
  )
  expect(section?.label).toBe('Overrides')
  expect(section?.rows).toEqual([
    {
      label: 'Response overrides',
      value: 'choices[0].message.content: rewritten',
    },
  ])
  expect(buildOverridesSection({ po: ['ignored'] }, t)).toBeUndefined()
  expect(buildOverridesSection({}, t)).toBeUndefined()
})

test('reasoning section shows the caller intent and upstream observation', () => {
  const section = buildReasoningSection(
    {
      thinking_type: 'disabled',
      reasoning_content_seen: true,
      upstream_reasoning_tokens: 1024,
    },
    t
  )
  expect(section?.label).toBe('Reasoning')
  const rows = new Map(section?.rows.map((row) => [row.label, row.value]))
  expect(rows.get('Thinking type')).toBe('disabled')
  expect(rows.get('Reasoning content seen')).toBe('Yes')
  expect(rows.get('Upstream reasoning tokens')).toBe('1,024')
  expect(buildReasoningSection({}, t)).toBeUndefined()
})

test('capture section names the matched rule and the stored file', () => {
  const section = buildCaptureSection(
    {
      rule: 'kimi-200-sample',
      file: '2026-10-31/120000-kimi-200-sample-req-1.json',
    },
    t
  )
  expect(section?.label).toBe('Request capture')
  expect(section?.rows).toEqual([
    { label: 'Capture rule', value: 'kimi-200-sample' },
    {
      label: 'Capture file',
      value: '2026-10-31/120000-kimi-200-sample-req-1.json',
    },
  ])
  expect(buildCaptureSection(undefined, t)).toBeUndefined()
})

test('stream closure rows expose terminal tail and tool-call closure', () => {
  const rows = buildStreamClosureRows(
    {
      stream_status: {
        closed_after_terminal: true,
        end_reason: 'terminal_tail_grace',
        error_count: 2,
      },
    },
    true,
    t
  )
  expect(rows).toEqual([
    { label: 'Closed after terminal finish', value: 'Yes' },
    { label: 'Closed as tool calls', value: 'Yes' },
    { label: 'Stream end reason', value: 'terminal_tail_grace' },
    { label: 'Soft Errors', value: '2' },
  ])
})
