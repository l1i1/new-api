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

import type { PricingModel } from '../../types'
import { formatDynamicUnitPrice } from '../dynamic-price'
import {
  formatPricingCurrencyFromUSD,
  formatPrice,
  formatRequestPrice,
} from '../price'

const tokenModel: PricingModel = {
  id: 1,
  model_name: 'test-token-model',
  quota_type: 0,
  model_ratio: 0.5,
  completion_ratio: 1,
  enable_groups: ['default'],
}

const requestModel: PricingModel = {
  id: 2,
  model_name: 'test-request-model',
  quota_type: 1,
  model_ratio: 1,
  completion_ratio: 1,
  model_price: 1,
  enable_groups: ['default'],
}

describe('pricing currency display', () => {
  test('formats the same USD token price in CNY and USD', () => {
    assert.equal(
      formatPrice(tokenModel, 'input', 'M', false, 1, 7, undefined, 'CNY'),
      '¥7'
    )
    assert.equal(
      formatPrice(tokenModel, 'input', 'M', false, 1, 7, undefined, 'USD'),
      '$1'
    )
  })

  test('applies recharge pricing before the selected currency is formatted', () => {
    assert.equal(
      formatRequestPrice(requestModel, true, 4, 7, undefined, 'CNY'),
      '¥4'
    )
    assert.equal(
      formatRequestPrice(requestModel, true, 4, 7, undefined, 'USD'),
      '$0.5714'
    )
  })

  test('uses the selected currency for dynamic pricing values', () => {
    assert.equal(
      formatDynamicUnitPrice(1, {
        tokenUnit: 'M',
        usdExchangeRate: 7,
        displayCurrency: 'CNY',
      }),
      '¥7'
    )
    assert.equal(
      formatDynamicUnitPrice(1, {
        tokenUnit: 'M',
        usdExchangeRate: 7,
        displayCurrency: 'USD',
      }),
      '$1'
    )
  })

  test('keeps tiny non-zero USD prices visible', () => {
    assert.equal(
      formatPricingCurrencyFromUSD(0.000000001, 'USD', 7, {
        digitsLarge: 4,
        digitsSmall: 6,
      }),
      '$0.000001'
    )
  })
})
