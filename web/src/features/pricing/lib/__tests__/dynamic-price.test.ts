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
import type { ParsedTier } from '../billing-expr'
import {
  getDynamicPriceEntries,
  getDynamicPricingSummary,
} from '../dynamic-price'

const tier: ParsedTier = {
  label: 'base',
  conditions: [],
  inputPrice: 1,
  outputPrice: 2,
}

describe('dynamic pricing group display', () => {
  test('keeps the base price when no group discount applies', () => {
    const [entry] = getDynamicPriceEntries(tier, {
      tokenUnit: 'M',
      usdExchangeRate: 7,
      displayCurrency: 'CNY',
    })

    assert.equal(entry.formatted, '¥7')
    assert.equal(entry.original, undefined)
  })

  test('returns the undiscounted price for a discounted group', () => {
    const [entry] = getDynamicPriceEntries(tier, {
      tokenUnit: 'M',
      usdExchangeRate: 7,
      displayCurrency: 'CNY',
      groupRatioMultiplier: 0.3,
    })

    assert.equal(entry.formatted, '¥2.1')
    assert.equal(entry.original, '¥7')
  })

  test('selects the off-peak tier on Sunday for time-based pricing', () => {
    const model = {
      billing_mode: 'tiered_expr',
      billing_expr:
        'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 ? tier("pro_peak", p * 1 + c * 2) : tier("pro_offpeak", p * 0.5 + c * 1)',
    } as PricingModel

    const summary = getDynamicPricingSummary(model, {
      tokenUnit: 'M',
      usdExchangeRate: 7,
      displayCurrency: 'CNY',
      now: new Date('2026-08-23T04:00:00Z'),
    })

    assert.equal(summary?.tier?.label, 'pro_offpeak')
    assert.equal(summary?.primaryEntries[0]?.formatted, '¥3.5')
  })

  test('selects the peak tier on a weekday for time-based pricing', () => {
    const model = {
      billing_mode: 'tiered_expr',
      billing_expr:
        'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 ? tier("pro_peak", p * 1 + c * 2) : tier("pro_offpeak", p * 0.5 + c * 1)',
    } as PricingModel

    const summary = getDynamicPricingSummary(model, {
      tokenUnit: 'M',
      usdExchangeRate: 7,
      displayCurrency: 'CNY',
      now: new Date('2026-08-24T04:00:00Z'),
    })

    assert.equal(summary?.tier?.label, 'pro_peak')
  })

  const deepseekExpr =
    'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18)) ? tier("flash_peak", p * 0.30 + c * 1.20 + cr * 0.006) : tier("flash_offpeak", p * 0.15 + c * 0.60 + cr * 0.003)'

  test('parses a parenthesized compound time condition instead of falling back to the raw expression', () => {
    const model = {
      billing_mode: 'tiered_expr',
      billing_expr: deepseekExpr,
    } as PricingModel

    const summary = getDynamicPricingSummary(model, {
      tokenUnit: 'M',
      usdExchangeRate: 7,
      displayCurrency: 'CNY',
      now: new Date('2026-09-11T02:00:00Z'),
    })

    assert.equal(summary?.isSpecialExpression, false)
    assert.deepEqual(
      summary?.tiers.map((tier) => tier.label),
      ['flash_peak', 'flash_offpeak']
    )
    assert.equal(summary?.tier?.label, 'flash_peak')
    assert.equal(summary?.primaryEntries[0]?.value, 0.3)
  })

  test('evaluates the off-peak branch of a parenthesized compound time condition', () => {
    const model = {
      billing_mode: 'tiered_expr',
      billing_expr: deepseekExpr,
    } as PricingModel

    const summary = getDynamicPricingSummary(model, {
      tokenUnit: 'M',
      usdExchangeRate: 7,
      displayCurrency: 'CNY',
      now: new Date('2026-09-11T04:00:00Z'),
    })

    assert.equal(summary?.tier?.label, 'flash_offpeak')
    assert.equal(summary?.primaryEntries[0]?.value, 0.15)
  })
})
