import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { formatCNY, formatQuotaMoney } from '../business-format'

const report = {
  quota_per_unit: 500_000,
  cny_per_usd: 7,
} as Parameters<typeof formatQuotaMoney>[1]

describe('business analysis currency formatting', () => {
  test('renders exactly one user-selected currency', () => {
    assert.equal(formatQuotaMoney(500_000, report, 'CNY'), '¥7.00')
    assert.equal(formatQuotaMoney(500_000, report, 'USD'), '$1.00')
    assert.equal(formatCNY(14, report, 'CNY'), '¥14.00')
    assert.equal(formatCNY(14, report, 'USD'), '$2.00')
  })
})
