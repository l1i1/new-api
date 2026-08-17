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

import { formatGroupDiscount } from '../model-helpers'

describe('group discount formatting', () => {
  test('keeps up to two decimal places for Chinese discount labels', () => {
    assert.equal(formatGroupDiscount(0.15, 'zh'), '1.5折')
    assert.equal(formatGroupDiscount(0.075, 'zh'), '0.75折')
    assert.equal(formatGroupDiscount(0.2, 'zh'), '2折')
  })

  test('keeps up to two decimal places for English discount labels', () => {
    assert.equal(formatGroupDiscount(0.007, 'en'), '99.3% off')
    assert.equal(formatGroupDiscount(0.125, 'en'), '87.5% off')
    assert.equal(formatGroupDiscount(0.2, 'en'), '80% off')
  })

  test('formats premium ratios with the same precision and hides one', () => {
    assert.equal(formatGroupDiscount(1, 'en'), undefined)
    assert.equal(formatGroupDiscount(1.5, 'en'), 'x1.5')
    assert.equal(formatGroupDiscount(2.5, 'zh'), 'x2.5')
  })
})
