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
import { describe, expect, test } from 'vitest'

import type { LogOtherData } from '../../types'
import {
  buildLogObservability,
  buildObservabilityContinuation,
  buildObservabilityFit,
  buildObservabilityNode,
  buildObservabilityPayload,
  formatBytes,
  formatCount,
} from '../observability'

/** Keys render as themselves, so assertions read the raw i18n keys. */
const t = (key: string) => key

const fullOther: LogOtherData = {
  admin_info: {
    node_name: 'i-2ze0abcdef',
    body_bytes: {
      request: 1234,
      upstream_request: 1250,
      response: 56789,
    },
    continuation: {
      modes: ['length'],
      rounds: 2,
      max_steps: 2,
      finish: 'stop',
      extra_completion_tokens: 4096,
    },
  },
  fit: {
    family: 'kimi-k3',
    marks: ['tools', 'history'],
    rules: ['r12'],
    policy: 0,
    baseline: 'a5615b8de20971fd',
  },
}

describe('formatBytes', () => {
  test.each([
    { name: 'absent', input: undefined, expected: undefined },
    { name: 'null', input: null, expected: undefined },
    { name: 'NaN', input: Number.NaN, expected: undefined },
    { name: 'zero', input: 0, expected: '0 B' },
    { name: 'single byte', input: 1, expected: '1 B' },
    { name: 'below one KiB', input: 1023, expected: '1023 B' },
    { name: 'exactly one KiB', input: 1024, expected: '1 KB' },
    { name: 'fractional KiB', input: 1234, expected: '1.2 KB' },
    { name: 'one and a half KiB', input: 1536, expected: '1.5 KB' },
    { name: 'one MiB', input: 1024 * 1024, expected: '1 MB' },
    { name: 'rounds up to one MiB', input: 1024 * 1024 - 1, expected: '1 MB' },
    { name: 'ten MiB', input: 10 * 1024 * 1024, expected: '10 MB' },
    { name: 'gigabytes', input: 5 * 1024 ** 3, expected: '5 GB' },
    { name: 'negative', input: -2048, expected: '-2 KB' },
  ])('formats $name', ({ input, expected }) => {
    expect(formatBytes(input)).toBe(expected)
  })
})

describe('formatCount', () => {
  test.each([
    { input: 0, expected: '0' },
    { input: 999, expected: '999' },
    { input: 4096, expected: '4,096' },
    { input: 1234567, expected: '1,234,567' },
  ])('groups $input', ({ input, expected }) => {
    expect(formatCount(input)).toBe(expected)
  })
})

describe('buildLogObservability', () => {
  test('builds the full display model', () => {
    const model = buildLogObservability(fullOther, t)

    expect(model?.nodeName).toBe('i-2ze0abcdef')
    expect(model?.fit?.family).toBe('kimi-k3')
    expect(model?.fit?.marks).toEqual(['tools', 'history'])
    expect(model?.fit?.rules).toEqual(['r12'])
    expect(model?.fit?.policy).toBe('0')
    expect(model?.fit?.baseline).toBe('a5615b8de20971fd')
    expect(model?.fit?.rows).toEqual([
      { label: 'Fit Family', value: 'kimi-k3' },
      { label: 'Fit Behaviours', value: 'tools, history' },
      { label: 'Fit Rules', value: 'r12' },
    ])

    expect(model?.payload?.request).toBe(1234)
    expect(model?.payload?.upstreamRequest).toBe(1250)
    expect(model?.payload?.response).toBe(56789)
    expect(model?.payload?.compact).toBe('1.2 KB → 55.5 KB')
    expect(model?.payload?.rows).toEqual([
      { label: 'Request Body', value: '1.2 KB' },
      { label: 'Upstream Request Body', value: '1.2 KB' },
      { label: 'Response Body', value: '55.5 KB' },
    ])

    expect(model?.continuation?.rounds).toBe(2)
    expect(model?.continuation?.maxSteps).toBe(2)
    expect(model?.continuation?.badge).toBe('Continued ×2')
    expect(model?.continuation?.tooltipRows).toEqual([
      { label: 'Continuation Rounds', value: '2' },
      { label: 'Max steps', value: '2' },
      { label: 'Continuation Mode', value: 'length' },
      { label: 'Finish', value: 'stop' },
      { label: 'Extra Completion Tokens', value: '4,096' },
    ])
    expect(model?.continuation?.detailRows).toEqual([
      { label: 'Continuation Rounds', value: '2' },
      { label: 'Max steps', value: '2' },
      { label: 'Continuation', value: 'length' },
      { label: 'Finish', value: 'stop' },
      { label: 'Continuation Tokens', value: '4,096' },
    ])
  })

  test.each([
    { name: 'null other', other: null },
    { name: 'empty other', other: {} },
    { name: 'empty admin_info', other: { admin_info: {} } },
    {
      name: 'zero-only payload sizes and no other field',
      other: {
        admin_info: {
          body_bytes: { request: 0, upstream_request: 0, response: 0 },
        },
      },
    },
    { name: 'fit without family, marks or rules', other: { fit: {} } },
  ])('renders nothing for $name', ({ other }) => {
    expect(buildLogObservability(other, t)).toBeUndefined()
  })

  test('omits fields that are absent while keeping present ones', () => {
    const model = buildLogObservability(
      {
        admin_info: { node_name: 'node-a' },
        fit: { family: 'kimi-k3' },
      },
      t
    )
    expect(model?.nodeName).toBe('node-a')
    expect(model?.fit?.rows).toEqual([
      { label: 'Fit Family', value: 'kimi-k3' },
    ])
    expect(model?.payload).toBeUndefined()
    expect(model?.continuation).toBeUndefined()
  })
})

describe('buildObservabilityNode', () => {
  test.each([
    { name: 'trims whitespace', input: ' node-a ', expected: 'node-a' },
    { name: 'treats blanks as absent', input: '   ', expected: undefined },
    {
      name: 'ignores nested root_info only',
      input: undefined,
      expected: undefined,
    },
  ])('$name', ({ input, expected }) => {
    expect(
      buildObservabilityNode({
        admin_info: { node_name: input },
        root_info: { node_name: 'root-node' },
      })
    ).toBe(expected)
  })
})

describe('buildObservabilityPayload', () => {
  test('marks a missing side with an em dash', () => {
    const payload = buildObservabilityPayload({ response: 2048 }, t)
    expect(payload?.compact).toBe('— → 2 KB')
    expect(payload?.rows).toEqual([{ label: 'Response Body', value: '2 KB' }])
  })

  test.each([
    { name: 'zero sizes', input: { request: 0, response: 0 } },
    { name: 'negative sizes', input: { request: -1 } },
    { name: 'non-numeric sizes', input: { request: Number.NaN } },
  ])('treats $name as unmeasured', ({ input }) => {
    expect(buildObservabilityPayload(input, t)).toBeUndefined()
  })
})

describe('buildObservabilityFit', () => {
  test('keeps policy and baseline for the popover footer', () => {
    const fit = buildObservabilityFit(
      {
        family: ' kimi-k3 ',
        marks: ['tools', ' tools ', ''],
        rules: ['r12'],
        policy: 'v12',
        baseline: ' a5615b8 ',
      },
      t
    )
    expect(fit?.family).toBe('kimi-k3')
    expect(fit?.marks).toEqual(['tools', 'tools'])
    expect(fit?.policy).toBe('v12')
    expect(fit?.baseline).toBe('a5615b8')
  })

  test('drops an empty policy', () => {
    expect(
      buildObservabilityFit({ family: 'kimi-k3', policy: '' }, t)?.policy
    ).toBeUndefined()
  })
})

describe('buildObservabilityContinuation', () => {
  test('keeps unknown mode ids raw', () => {
    const continuation = buildObservabilityContinuation(
      { modes: ['mystery'], rounds: 1, finish: 'length' },
      t
    )
    expect(continuation?.badge).toBe('Continued ×1')
    expect(continuation?.tooltipRows).toEqual([
      { label: 'Continuation Rounds', value: '1' },
      { label: 'Continuation Mode', value: 'mystery' },
      { label: 'Finish', value: 'length' },
    ])
  })

  test('reports extra completion tokens', () => {
    const continuation = buildObservabilityContinuation(
      { rounds: 3, extra_completion_tokens: 12345 },
      t
    )
    expect(continuation?.tooltipRows).toEqual([
      { label: 'Continuation Rounds', value: '3' },
      { label: 'Extra Completion Tokens', value: '12,345' },
    ])
    expect(continuation?.detailRows).toEqual([
      { label: 'Continuation Rounds', value: '3' },
      { label: 'Continuation Tokens', value: '12,345' },
    ])
  })

  test.each([
    { name: 'absent', input: undefined },
    { name: 'zero rounds', input: { rounds: 0 } },
    { name: 'no rounds', input: { modes: ['length'] } },
  ])('is undefined for $name', ({ input }) => {
    expect(buildObservabilityContinuation(input, t)).toBeUndefined()
  })
})
