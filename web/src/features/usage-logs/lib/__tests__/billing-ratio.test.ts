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
*/
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { getEffectiveBillingRatio } from '../format'

describe('usage-log billing ratios', () => {
  test('uses a user-specific ratio over the regular group ratio', () => {
    assert.equal(
      getEffectiveBillingRatio({ user_group_ratio: 1.5, group_ratio: 2 }),
      1.5
    )
  })

  test('uses the regular group ratio and defaults to one', () => {
    assert.equal(getEffectiveBillingRatio({ group_ratio: 2 }), 2)
    assert.equal(getEffectiveBillingRatio({ group_ratio: 0 }), 0)
    assert.equal(getEffectiveBillingRatio(null), 1)
  })
})
