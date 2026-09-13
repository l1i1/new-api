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

import type { InvoiceableOrder } from '../../types'
import {
  calculateInvoiceFee,
  calculateInvoiceFeeQuota,
  canSubmitInvoice,
  hasMixedCurrency,
  isBelowMinimum,
  minimumForCurrency,
  reasonRequired,
  resolveDefaultEmail,
  sumByCurrency,
  sumOrderAmounts,
} from '../apply'

function order(id: number, amount: number, currency: string): InvoiceableOrder {
  return {
    order_type: 'topup',
    order_id: id,
    trade_no: `order-${id}`,
    amount,
    currency,
    payment_method: 'epay',
    create_time: 0,
  }
}

describe('resolveDefaultEmail', () => {
  test('account email always wins over the saved profile email', () => {
    assert.equal(
      resolveDefaultEmail('account@example.com', 'old@example.com'),
      'account@example.com'
    )
  })

  test('saved profile email is the fallback when no account email exists', () => {
    assert.equal(
      resolveDefaultEmail('', 'old@example.com'),
      'old@example.com'
    )
  })

  test('empty result when neither email exists', () => {
    assert.equal(resolveDefaultEmail('', ''), '')
  })

  test('whitespace-only account email is treated as absent', () => {
    assert.equal(
      resolveDefaultEmail('   ', 'old@example.com'),
      'old@example.com'
    )
  })
})

describe('sumOrderAmounts', () => {
  test('sums amounts without float drift', () => {
    assert.equal(
      sumOrderAmounts([order(1, 0.1, 'USD'), order(2, 0.2, 'USD')]),
      0.30000000000000004
    )
  })
})

describe('hasMixedCurrency', () => {
  test('false for a single currency', () => {
    assert.equal(
      hasMixedCurrency([order(1, 10, 'CNY'), order(2, 20, 'CNY')]),
      false
    )
  })

  test('true for mixed currencies', () => {
    assert.equal(
      hasMixedCurrency([order(1, 10, 'CNY'), order(2, 20, 'USD')]),
      true
    )
  })

  test('false for no orders', () => {
    assert.equal(hasMixedCurrency([]), false)
  })
})

describe('isBelowMinimum', () => {
  test('exact equality satisfies the minimum', () => {
    assert.equal(isBelowMinimum(100, 100), false)
  })

  test('below minimum is detected', () => {
    assert.equal(isBelowMinimum(99.99, 100), true)
  })

  test('zero minimum never blocks', () => {
    assert.equal(isBelowMinimum(0, 0), false)
  })
})

describe('calculateInvoiceFee', () => {
  test('applies the rate and rounds to 2 decimal places', () => {
    assert.equal(calculateInvoiceFee(100, 0.06), 6)
    assert.equal(calculateInvoiceFee(50, 0.06), 3)
    assert.equal(calculateInvoiceFee(99.99, 0.06), 6)
    assert.equal(calculateInvoiceFee(100, 0), 0)
  })
})

describe('calculateInvoiceFeeQuota', () => {
  test('mirrors the backend quota conversion', () => {
    assert.equal(calculateInvoiceFeeQuota(100, 0.06, 500000), 3000000)
    assert.equal(calculateInvoiceFeeQuota(50, 0.06, 500000), 1500000)
  })
})

describe('canSubmitInvoice', () => {
  const base = {
    selectedCount: 1,
    mixedCurrency: false,
    belowMinimum: false,
    accountEmailUnavailable: false,
    insufficientBalance: false,
    submitting: false,
  }

  test('allows submission when every gate passes', () => {
    assert.equal(canSubmitInvoice(base), true)
  })

  test('blocks without selected orders', () => {
    assert.equal(canSubmitInvoice({ ...base, selectedCount: 0 }), false)
  })

  test('blocks mixed currencies', () => {
    assert.equal(canSubmitInvoice({ ...base, mixedCurrency: true }), false)
  })

  test('blocks below-minimum selections', () => {
    assert.equal(canSubmitInvoice({ ...base, belowMinimum: true }), false)
  })

  test('blocks when the account email is unavailable', () => {
    assert.equal(
      canSubmitInvoice({ ...base, accountEmailUnavailable: true }),
      false
    )
  })

  test('blocks when the balance is insufficient for the fee', () => {
    assert.equal(
      canSubmitInvoice({ ...base, insufficientBalance: true }),
      false
    )
  })

  test('blocks while a submission is in flight', () => {
    assert.equal(canSubmitInvoice({ ...base, submitting: true }), false)
  })
})

describe('reasonRequired', () => {
  test('individual invoices require a reason', () => {
    assert.equal(reasonRequired('individual'), true)
  })

  test('organization invoices do not require a reason', () => {
    assert.equal(reasonRequired('organization'), false)
  })
})

describe('sumByCurrency', () => {
  test('groups amounts by settlement currency preserving first-appearance order', () => {
    assert.deepEqual(
      sumByCurrency([
        order(1, 21, 'CNY'),
        order(2, 1.02, 'USD'),
        order(3, 21, 'CNY'),
      ]),
      [
        { currency: 'CNY', total: 42 },
        { currency: 'USD', total: 1.02 },
      ]
    )
  })

  test('rounds floating-point drift to 2 decimals', () => {
    assert.deepEqual(
      sumByCurrency([order(1, 0.1, 'USD'), order(2, 0.2, 'USD')]),
      [{ currency: 'USD', total: 0.3 }]
    )
  })

  test('empty selection yields no groups', () => {
    assert.deepEqual(sumByCurrency([]), [])
  })
})

describe('minimumForCurrency', () => {
  test('CNY selections use the configured minimum as-is', () => {
    assert.equal(minimumForCurrency(49, 'CNY', 7), 49)
  })

  test('USD selections divide the CNY minimum by the platform rate', () => {
    assert.equal(minimumForCurrency(49, 'USD', 7), 7)
  })

  test('keeps the exact quotient so the gate cannot diverge from the backend', () => {
    // 50/7 = 7.142857...: pre-rounding to 7.14 would let a $7.14 selection
    // pass the client gate while the backend (7.14*7 = 49.98 < 50) rejects it.
    assert.ok(Math.abs(minimumForCurrency(50, 'USD', 7) - 50 / 7) < 1e-12)
    assert.ok(minimumForCurrency(50, 'USD', 7) > 7.14)
  })

  test('invalid rate fails closed to the raw CNY number (mirrors backend)', () => {
    for (const rate of [0, -7, Number.NaN, Number.POSITIVE_INFINITY]) {
      assert.equal(minimumForCurrency(49, 'USD', rate), 49)
    }
  })

  test('zero minimum disables the gate for every currency', () => {
    assert.equal(minimumForCurrency(0, 'USD', 7), 0)
    assert.equal(minimumForCurrency(0, 'CNY', 7), 0)
  })
})
