import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { formatNumber } from '@/lib/format'

import { formatInvoiceAmount } from '../format'

describe('formatInvoiceAmount', () => {
  test('renders the settlement currency without any conversion', () => {
    assert.equal(formatInvoiceAmount(21, 'CNY'), '¥21')
    assert.equal(formatInvoiceAmount(1.0219, 'USD'), '$1.02')
  })

  test('USD never renders with a CNY symbol regardless of input case', () => {
    assert.equal(formatInvoiceAmount(1.02, 'usd'), '$1.02')
  })

  test('trailing zeros are stripped (0-2 fraction digits)', () => {
    assert.equal(formatInvoiceAmount(42, 'CNY'), '¥42')
    assert.equal(formatInvoiceAmount(42.5, 'USD'), '$42.5')
  })

  test('empty currency falls back to a bare number', () => {
    assert.equal(formatInvoiceAmount(43.02, ''), formatNumber(43.02))
  })

  test('invalid currency codes fall back to CODE + fixed-2 amount', () => {
    assert.equal(formatInvoiceAmount(1.5, 'NOPE'), 'NOPE 1.50')
  })
})
