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

import { getTieredBillingSummary } from '../format'

const deepseekExpr =
  'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18)) ? tier("flash_peak", p * 0.30 + c * 1.20 + cr * 0.006) : tier("flash_offpeak", p * 0.15 + c * 0.60 + cr * 0.003)'

function b64(value: string): string {
  return Buffer.from(value, 'binary').toString('base64')
}

describe('usage-log tiered billing summary', () => {
  test('resolves the recorded tier of a parenthesized compound time condition', () => {
    const summary = getTieredBillingSummary({
      billing_mode: 'tiered_expr',
      expr_b64: b64(deepseekExpr),
      matched_tier: 'flash_offpeak',
      cache_tokens: 768,
    })

    assert.ok(summary)
    assert.equal(summary.tier.label, 'flash_offpeak')
    assert.deepEqual(
      summary.tiers.map((tier) => tier.label),
      ['flash_peak', 'flash_offpeak']
    )
    assert.deepEqual(
      summary.priceEntries.map((entry) => [entry.field, entry.price]),
      [
        ['inputPrice', 0.15],
        ['outputPrice', 0.6],
        ['cacheReadPrice', 0.003],
      ]
    )
  })

  test('returns null when the recorded tier is not in the parsed expression', () => {
    const summary = getTieredBillingSummary({
      billing_mode: 'tiered_expr',
      expr_b64: b64(deepseekExpr),
      matched_tier: 'unrelated_tier',
    })

    assert.equal(summary, null)
  })
})
