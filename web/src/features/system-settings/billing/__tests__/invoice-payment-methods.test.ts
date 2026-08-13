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

import {
  getPaymentMethodOptions,
  normalizePaymentMethodValues,
} from '../invoice-payment-methods'

describe('invoice payment method settings', () => {
  test('normalizes the saved allowlist case-insensitively', () => {
    assert.deepEqual(
      normalizePaymentMethodValues([' Stripe ', 'alipay', 'STRIPE', '']),
      ['alipay', 'stripe']
    )
  })

  test('includes configured and previously saved custom methods', () => {
    const options = getPaymentMethodOptions(
      '[{"name":"Bank Transfer","type":"bank_transfer"}]',
      ['legacy_method'],
      (key) => key
    )
    const values = new Set(options.map((option) => option.value))

    assert.equal(values.has('alipay'), true)
    assert.equal(values.has('bank_transfer'), true)
    assert.equal(values.has('legacy_method'), true)
  })
})
