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

import { evalExprLocally, resolveTieredEditorMode } from '../tier-expr'

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
  test('keeps complex billing expressions in expression-editor mode', () => {
    const deepSeekExpr =
      '((hour("UTC") >= 1 && hour("UTC") < 4) || (hour("UTC") >= 6 && hour("UTC") < 10)) ? tier("peak", p * 0.44 + cr * 0.014 + c * 1.32) : tier("off_peak", p * 0.22 + cr * 0.007 + c * 0.66)'

    assert.equal(resolveTieredEditorMode(deepSeekExpr), 'raw')
    assert.equal(
      resolveTieredEditorMode('tier("base", p * 2 + c * 4)'),
      'visual'
    )
    assert.equal(resolveTieredEditorMode(''), 'visual')
  })

  test('matches backend timezone, weekday, month, and day semantics', () => {
    const now = new Date('2026-01-01T00:00:00Z')
    const result = evalExprLocally(
      'tier("base", hour("Asia/Shanghai") * 100000 + minute("Asia/Shanghai") * 10000 + weekday("America/New_York") * 1000 + month("America/New_York") * 10 + day("America/New_York"))',
      100,
      0,
      emptyExtras,
      now
    )

    assert.equal(result.error, null)
    assert.equal(result.cost, 803151)
    assert.equal(result.matchedTier, 'base')
  })

  test('falls back to UTC for invalid and empty timezones', () => {
    const now = new Date('2026-01-01T23:45:00Z')
    const result = evalExprLocally(
      'tier("base", hour("Invalid/Zone") * 100 + minute("") + weekday("Invalid/Zone"))',
      0,
      0,
      emptyExtras,
      now
    )

    assert.equal(result.error, null)
    assert.equal(result.cost, 2349)
    assert.equal(result.matchedTier, 'base')
  })

  test('rejects Local timezone instead of showing a misleading preview', () => {
    const result = evalExprLocally(
      'tier("base", hour("Local"))',
      0,
      0,
      emptyExtras,
      new Date('2026-01-01T00:00:00Z')
    )

    assert.equal(result.cost, 0)
    assert.equal(result.matchedTier, '')
    assert.match(result.error ?? '', /Local timezone is not supported/)
  })

  test('evaluates the DeepSeek peak/off-peak expression without preview errors', () => {
    const result = evalExprLocally(
      '((hour("UTC") >= 1 && hour("UTC") < 4) || (hour("UTC") >= 6 && hour("UTC") < 10)) ? tier("peak", p * 0.44 + cr * 0.014 + c * 1.32) : tier("off_peak", p * 0.22 + cr * 0.007 + c * 0.66)',
      1000,
      500,
      { ...emptyExtras, cacheReadTokens: 200 },
      new Date('2026-01-01T02:00:00Z')
    )

    assert.equal(result.error, null)
    assert.equal(result.cost, 1102.8)
    assert.equal(result.matchedTier, 'peak')
  })
})
