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
import { afterEach, describe, test } from 'node:test'

import {
  getDefaultDisplayCurrency,
  useCurrencyDisplayStore,
} from '../../stores/currency-display-store'
import { useSystemConfigStore } from '../../stores/system-config-store'
import {
  formatBillingCurrencyFromUSD,
  formatCnyAmount,
  formatCnyFromUSD,
  formatCurrencyFromUSD,
  formatPaymentAmount,
} from '../currency'

const originalCurrencyConfig = useSystemConfigStore.getState().config.currency
const originalDisplayCurrency = useCurrencyDisplayStore.getState().currency

afterEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: originalCurrencyConfig,
  })
  useCurrencyDisplayStore.getState().setCurrency(originalDisplayCurrency)
})

describe('user currency display preference', () => {
  test('defaults Chinese browsers to CNY and English browsers to USD', () => {
    assert.equal(getDefaultDisplayCurrency('zh-CN'), 'CNY')
    assert.equal(getDefaultDisplayCurrency('zh-TW'), 'CNY')
    assert.equal(getDefaultDisplayCurrency('en-US'), 'USD')
    assert.equal(getDefaultDisplayCurrency('en-GB'), 'USD')
  })

  test('converts balances, API costs, and CNY recharge previews together', () => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...originalCurrencyConfig,
        usdExchangeRate: 7,
      },
    })

    useCurrencyDisplayStore.getState().setCurrency('CNY')
    assert.equal(formatCurrencyFromUSD(1), '¥7')
    assert.equal(formatBillingCurrencyFromUSD(1), '¥7')
    assert.equal(formatCnyFromUSD(1), '¥7')
    assert.equal(formatCnyAmount(7), '¥7')

    useCurrencyDisplayStore.getState().setCurrency('USD')
    assert.equal(formatCurrencyFromUSD(1), '$1')
    assert.equal(formatBillingCurrencyFromUSD(1), '$1')
    assert.equal(formatCnyFromUSD(1), '$1')
    assert.equal(formatCnyAmount(7), '$1')
  })

  test('converts known payment currencies and preserves unknown source currencies', () => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...originalCurrencyConfig,
        usdExchangeRate: 7,
      },
    })

    useCurrencyDisplayStore.getState().setCurrency('USD')
    assert.equal(formatPaymentAmount(14, 'CNY'), '$2')
    assert.equal(formatPaymentAmount(2, 'USD'), '$2')
    assert.equal(formatPaymentAmount(2, 'EUR'), 'EUR 2')
    assert.equal(formatPaymentAmount(2, undefined), null)

    useCurrencyDisplayStore.getState().setCurrency('CNY')
    assert.equal(formatPaymentAmount(14, 'CNY'), '¥14')
    assert.equal(formatPaymentAmount(2, 'USD'), '¥14')
    assert.equal(formatPaymentAmount(2, 'EUR'), 'EUR 2')
  })
})
