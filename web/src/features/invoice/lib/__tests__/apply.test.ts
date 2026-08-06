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
  canSubmitInvoice,
  hasMixedCurrency,
  isBelowMinimum,
  reasonRequired,
  resolveDefaultEmail,
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

describe('canSubmitInvoice', () => {
  const base = {
    selectedCount: 1,
    mixedCurrency: false,
    belowMinimum: false,
    accountEmailUnavailable: false,
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
