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
import { requestPaymentAmount } from './use-payment'

describe('payment amount routing', () => {
  test('uses the dedicated Waffo amount calculator', async () => {
    const calls: string[] = []
    const amount = await requestPaymentAmount(120, PAYMENT_TYPES.WAFFO, {
      regular: async () => {
        calls.push('regular')
        return { success: true, data: '1' }
      },
      stripe: async () => {
        calls.push('stripe')
        return { success: true, data: '2' }
      },
      waffo: async (request) => {
        calls.push(`waffo:${request.amount}`)
        return { success: true, data: '18.75' }
      },
      waffoPancake: async () => {
        calls.push('pancake')
        return { success: true, data: '4' }
      },
    })

    expect(amount).toBe(18.75)
    expect(calls).toEqual(['waffo:120'])
  })

  test('keeps bare Pancake checkout unrestricted', async () => {
    let request:
      | { amount: number; currency?: string; payment_method?: string }
      | undefined
    const amount = await requestPaymentAmount(
      120,
      PAYMENT_TYPES.WAFFO_PANCAKE,
      {
        regular: async () => ({ success: false, data: '' }),
        stripe: async () => ({ success: false, data: '' }),
        waffo: async () => ({ success: false, data: '' }),
        waffoPancake: async (value) => {
          request = value
          return { success: true, data: '17.14' }
        },
      },
      'CNY'
    )

    assert.equal(amount, 17.14)
    assert.deepEqual(request, { amount: 120, currency: 'CNY' })
  })

  test('forwards the Pancake method selected by the processing identifier', async () => {
    let request:
      | { amount: number; currency?: string; payment_method?: string }
      | undefined
    await requestPaymentAmount(
      120,
      'waffo_pancake:googlepay',
      {
        regular: async () => ({ success: false, data: '' }),
        stripe: async () => ({ success: false, data: '' }),
        waffo: async () => ({ success: false, data: '' }),
        waffoPancake: async (value) => {
          request = value
          return { success: true, data: '17.14' }
        },
      },
      'CNY'
    )

    assert.deepEqual(request, {
      amount: 120,
      currency: 'CNY',
      payment_method: 'google_pay',
    })
  })
})
