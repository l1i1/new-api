/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

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

import { evalExprLocally } from '../tier-expr'

const emptyExtras = {
  cacheReadTokens: 0,
  cacheCreateTokens: 0,
  cacheCreate1hTokens: 0,
  imageTokens: 0,
  imageOutputTokens: 0,
  audioInputTokens: 0,
  audioOutputTokens: 0,
}

describe('local billing expression evaluator', () => {
  test('supports backend-compatible time functions', () => {
    const result = evalExprLocally(
      'tier("base", p) * (hour("UTC") >= 0 ? 1 : 2) * (minute("UTC") >= 0 ? 1 : 2) * (weekday("UTC") >= 0 ? 1 : 2) * (month("UTC") >= 1 ? 1 : 2) * (day("UTC") >= 1 ? 1 : 2)',
      100,
      0,
      emptyExtras
    )

    assert.equal(result.error, null)
    assert.equal(result.cost, 100)
    assert.equal(result.matchedTier, 'base')
  })

  test('evaluates the DeepSeek peak/off-peak expression without preview errors', () => {
    const result = evalExprLocally(
      '((hour("UTC") >= 1 && hour("UTC") < 4) || (hour("UTC") >= 6 && hour("UTC") < 10)) ? tier("peak", p * 0.44 + cr * 0.014 + c * 1.32) : tier("off_peak", p * 0.22 + cr * 0.007 + c * 0.66)',
      1000,
      500,
      { ...emptyExtras, cacheReadTokens: 200 }
    )

    assert.equal(result.error, null)
    assert.ok(result.cost > 0)
    assert.ok(
      result.matchedTier === 'peak' || result.matchedTier === 'off_peak'
    )
  })
})
