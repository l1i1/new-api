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

import { getInvoiceStatusConfig, INVOICE_STATUS_CONFIG } from '../status'

describe('invoice status configuration', () => {
  test('covers all six statuses with distinct badge variants', () => {
    const statuses = [
      'pending',
      'approved',
      'issuing',
      'issued',
      'rejected',
      'cancelled',
    ] as const
    const seenVariants = new Set<string>()
    for (const status of statuses) {
      const config = getInvoiceStatusConfig(status)
      const labelKey = config.labelKey as string
      assert.equal(typeof labelKey, 'string')
      assert.ok(labelKey.length > 0)
      const variant = config.variant as string
      seenVariants.add(variant)
    }
    assert.ok(seenVariants.has('success'), 'issued must render as success')
    assert.ok(seenVariants.has('danger'), 'rejected must render as danger')
    assert.ok(
      seenVariants.has('neutral'),
      'cancelled must render as neutral'
    )
  })

  test('maps each status to a distinct i18n label key', () => {
    const keys = Object.values(INVOICE_STATUS_CONFIG)
      .map((config) => config.labelKey)
      .filter((key): key is string => typeof key === 'string')
    assert.equal(new Set(keys).size, 6)
    for (const key of keys) {
      assert.ok(key.startsWith('Invoice status '), key)
    }
  })

  test('falls back to pending for an unknown status', () => {
    const config = getInvoiceStatusConfig('unknown' as never)
    assert.equal(config.labelKey, 'Invoice status pending')
  })
})
