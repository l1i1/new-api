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
import { describe, expect, it } from 'vitest'

import {
  buildTrafficCell,
  formatBytes,
  formatCompactNumber,
  formatExactBytes,
  formatInteger,
} from '../lib/traffic'
import type { TrafficSnapshot } from '../types'

const LOCALES = 'en-US'

const busyTraffic: TrafficSnapshot = {
  window_seconds: 60,
  rpm: 12,
  tpm: 4600,
  prompt_tpm: 1200,
  completion_tpm: 3400,
  request_bytes: 123456,
  response_bytes: 4567890,
  requests_total: 987654,
  prompt_tokens_total: 120000,
  completion_tokens_total: 340000,
  tokens_total: 460000,
  request_bytes_total: 12345678,
  response_bytes_total: 456789012,
}

describe('formatCompactNumber', () => {
  const cases: Array<[number | null | undefined, string]> = [
    [undefined, '-'],
    [null, '-'],
    [Number.NaN, '-'],
    [Number.POSITIVE_INFINITY, '-'],
    [-1, '-'],
    [0, '0'],
    [1, '1'],
    [99, '99'],
    [999, '999'],
    [1000, '1k'],
    [1200, '1.2k'],
    [4600, '4.6k'],
    [12500, '12.5k'],
    [99999, '100k'],
    [100000, '100k'],
    [123456, '123k'],
    [999999, '1M'],
    [1_000_000, '1M'],
    [3_400_000, '3.4M'],
    [999_999_999, '1B'],
    [1_200_000_000, '1.2B'],
  ]

  it.each(cases)('formats %s as %s', (value, expected) => {
    expect(formatCompactNumber(value)).toBe(expected)
  })
})

describe('formatInteger', () => {
  const cases: Array<[number | null | undefined, string]> = [
    [undefined, '-'],
    [null, '-'],
    [-3, '-'],
    [0, '0'],
    [12, '12'],
    [999, '999'],
    [1234, '1,234'],
  ]

  it.each(cases)('formats %s as %s', (value, expected) => {
    expect(formatInteger(value, LOCALES)).toBe(expected)
  })
})

describe('formatBytes', () => {
  const cases: Array<[number | null | undefined, string]> = [
    [undefined, '-'],
    [null, '-'],
    [Number.NaN, '-'],
    [-1, '-'],
    [0, '0 B'],
    [1, '1 B'],
    [512, '512 B'],
    [1023, '1,023 B'],
    [1024, '1 KB'],
    [1536, '1.5 KB'],
    [10 * 1024, '10 KB'],
    [1024 ** 2, '1 MB'],
    [4.5 * 1024 ** 2, '4.5 MB'],
    [1024 ** 3, '1 GB'],
    [1.5 * 1024 ** 3, '1.5 GB'],
  ]

  it.each(cases)('formats %s as %s', (value, expected) => {
    expect(formatBytes(value, LOCALES)).toBe(expected)
  })
})

describe('formatExactBytes', () => {
  const cases: Array<[number | null | undefined, string]> = [
    [undefined, '-'],
    [-1, '-'],
    [0, '0 B'],
    [999, '999 B'],
    [1234, '1,234 B'],
    [4567890, '4,567,890 B'],
  ]

  it.each(cases)('formats %s as %s', (value, expected) => {
    expect(formatExactBytes(value, LOCALES)).toBe(expected)
  })
})

describe('buildTrafficCell', () => {
  it('reports absent traffic as dashes instead of zeros', () => {
    for (const traffic of [undefined, null, {}, { window_seconds: 60 }]) {
      const cell = buildTrafficCell(traffic, LOCALES)
      expect(cell.hasTraffic).toBe(false)
      expect(cell.rpm).toBe('-')
      expect(cell.tpm).toBe('-')
      expect(cell.inbound).toBe('-')
      expect(cell.outbound).toBe('-')
      expect(cell.windowSeconds).toBeNull()
    }
  })

  it('keeps a genuinely idle node readable as zero', () => {
    const idle: TrafficSnapshot = {
      window_seconds: 60,
      rpm: 0,
      tpm: 0,
      prompt_tpm: 0,
      completion_tpm: 0,
      request_bytes: 0,
      response_bytes: 0,
      requests_total: 0,
      prompt_tokens_total: 0,
      completion_tokens_total: 0,
      tokens_total: 0,
      request_bytes_total: 0,
      response_bytes_total: 0,
    }
    const cell = buildTrafficCell(idle, LOCALES)
    expect(cell.hasTraffic).toBe(true)
    expect(cell.rpm).toBe('0')
    expect(cell.tpm).toBe('0')
    expect(cell.inbound).toBe('0 B')
    expect(cell.outbound).toBe('0 B')
    expect(cell.windowSeconds).toBe(60)
  })

  it('formats a busy node for the three columns and the tooltips', () => {
    const cell = buildTrafficCell(busyTraffic, LOCALES)
    expect(cell.hasTraffic).toBe(true)
    expect(cell.rpm).toBe('12')
    expect(cell.tpm).toBe('4.6k')
    expect(cell.inbound).toBe('120.6 KB')
    expect(cell.outbound).toBe('4.4 MB')
    expect(cell.rpmValue).toBe(12)
    expect(cell.tpmValue).toBe(4600)
    expect(cell.promptTokensValue).toBe(1200)
    expect(cell.completionTokensValue).toBe(3400)
    expect(cell.requestBytesValue).toBe(123456)
    expect(cell.responseBytesValue).toBe(4567890)
    expect(cell.windowSeconds).toBe(60)
  })

  it('formats millions and boundary counts compactly', () => {
    expect(
      buildTrafficCell({ ...busyTraffic, tpm: 3_400_000 }, LOCALES).tpm
    ).toBe('3.4M')
    expect(buildTrafficCell({ ...busyTraffic, rpm: 999 }, LOCALES).rpm).toBe(
      '999'
    )
    expect(buildTrafficCell({ ...busyTraffic, rpm: 1000 }, LOCALES).rpm).toBe(
      '1,000'
    )
    expect(
      buildTrafficCell({ ...busyTraffic, tpm: 1_000_000 }, LOCALES).tpm
    ).toBe('1M')
    expect(buildTrafficCell({ ...busyTraffic, tpm: 0 }, LOCALES).tpm).toBe('0')
  })

  it('keeps partial snapshots partially readable', () => {
    const cell = buildTrafficCell({ window_seconds: 60, rpm: 7 }, LOCALES)
    expect(cell.hasTraffic).toBe(true)
    expect(cell.rpm).toBe('7')
    expect(cell.tpm).toBe('-')
    expect(cell.inbound).toBe('-')
    expect(cell.outbound).toBe('-')
    expect(cell.windowSeconds).toBe(60)
  })

  it('treats malformed and negative counters as unknown', () => {
    const cell = buildTrafficCell(
      {
        window_seconds: 60,
        rpm: -1,
        tpm: Number.NaN,
        prompt_tpm: Number.POSITIVE_INFINITY,
        completion_tpm: -2,
        request_bytes: -100,
        response_bytes: 'nope' as unknown as number,
      },
      LOCALES
    )
    expect(cell.hasTraffic).toBe(false)
    expect(cell.rpm).toBe('-')
    expect(cell.tpm).toBe('-')
    expect(cell.inbound).toBe('-')
    expect(cell.outbound).toBe('-')
  })

  it('ignores only the malformed counters of an otherwise valid snapshot', () => {
    const cell = buildTrafficCell(
      { window_seconds: 60, rpm: 10, tpm: -1, request_bytes: 2048 },
      LOCALES
    )
    expect(cell.hasTraffic).toBe(true)
    expect(cell.rpm).toBe('10')
    expect(cell.tpm).toBe('-')
    expect(cell.inbound).toBe('2 KB')
    expect(cell.outbound).toBe('-')
  })
})
