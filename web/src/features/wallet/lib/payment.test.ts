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
import { describe, expect, test } from 'vitest'

import { PAYMENT_TYPES } from '../constants'
import {
  dispatchSelectedPayment,
  getWaffoPancakePaymentMethod,
  getWaffoPancakeProviderCurrency,
  isStandardEpayPayment,
  isStripePayment,
  isWaffoPayment,
  isWaffoPancakePayment,
} from './payment'

describe('payment type classification', () => {
  test('keeps Waffo and Waffo Pancake on their dedicated flows', () => {
    assert.equal(isWaffoPayment(PAYMENT_TYPES.WAFFO), true)
    assert.equal(isWaffoPayment(PAYMENT_TYPES.WAFFO_PANCAKE), false)
    assert.equal(isWaffoPancakePayment(PAYMENT_TYPES.WAFFO_PANCAKE), true)
    assert.equal(isWaffoPancakePayment('waffo_pancake:wechat'), true)
    assert.equal(isWaffoPancakePayment('waffo_pancake:googlepay'), true)
    assert.equal(isWaffoPancakePayment('waffo_pancake:applepay'), true)
    assert.equal(isWaffoPancakePayment('waffo_pancake:card'), true)
    assert.equal(isWaffoPancakePayment('waffo_pancake:alipay'), false)
    assert.equal(isWaffoPancakePayment(PAYMENT_TYPES.WAFFO), false)
    assert.equal(isStripePayment(PAYMENT_TYPES.STRIPE), true)
  })

  test('identifies generic payment methods as the standard EPay flow', () => {
    assert.equal(isStandardEpayPayment(PAYMENT_TYPES.ALIPAY), true)
    assert.equal(isStandardEpayPayment(PAYMENT_TYPES.WECHAT), true)
    assert.equal(isStandardEpayPayment('custom-epay'), true)
    assert.equal(isStandardEpayPayment(PAYMENT_TYPES.STRIPE), false)
    assert.equal(isStandardEpayPayment(PAYMENT_TYPES.CREEM), false)
    assert.equal(isStandardEpayPayment(PAYMENT_TYPES.WAFFO), false)
    assert.equal(isStandardEpayPayment(PAYMENT_TYPES.WAFFO_PANCAKE), false)
  })

  test('maps Pancake processing identifiers to provider methods and currency', () => {
    assert.equal(getWaffoPancakePaymentMethod('waffo_pancake'), null)
    assert.equal(
      getWaffoPancakePaymentMethod('waffo_pancake:wechat'),
      'wechat_pay'
    )
    assert.equal(
      getWaffoPancakePaymentMethod('waffo_pancake:googlepay'),
      'google_pay'
    )
    assert.equal(
      getWaffoPancakePaymentMethod('waffo_pancake:applepay'),
      'apple_pay'
    )
    assert.equal(getWaffoPancakePaymentMethod('waffo_pancake:card'), 'card')
    assert.equal(getWaffoPancakeProviderCurrency('CNY', null), 'USD')
    assert.equal(getWaffoPancakeProviderCurrency('CNY', 'wechat_pay'), 'CNY')
    assert.equal(getWaffoPancakeProviderCurrency('CNY', 'card'), 'USD')
    assert.equal(getWaffoPancakeProviderCurrency('USD', 'wechat_pay'), 'USD')
  })
})

describe('payment dispatch', () => {
  test('keeps the selected Waffo method index through confirmation', async () => {
    const calls: string[] = []
    const success = await dispatchSelectedPayment(
      { name: 'Waffo Card', type: PAYMENT_TYPES.WAFFO },
      120,
      3,
      {
        regular: async () => {
          calls.push('regular')
          return false
        },
        waffo: async (amount, index) => {
          calls.push(`waffo:${amount}:${index}`)
          return true
        },
        waffoPancake: async () => {
          calls.push('pancake')
          return false
        },
      }
    )

    expect(success).toBe(true)
    expect(calls).toEqual(['waffo:120:3'])
  })

  test('does not create a Waffo order without a selected method index', async () => {
    let called = false
    const success = await dispatchSelectedPayment(
      { name: 'Waffo Card', type: PAYMENT_TYPES.WAFFO },
      120,
      null,
      {
        regular: async () => false,
        waffo: async () => {
          called = true
          return true
        },
        waffoPancake: async () => false,
      }
    )

    expect(success).toBe(false)
    expect(called).toBe(false)
  })
})
