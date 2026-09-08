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
import type { InvoiceableOrder, InvoiceType } from '../types'

// ============================================================================
// Pure invoice apply-form helpers
// ============================================================================

/**
 * Resolve the delivery-email default. The current account email always wins; a
 * historically saved profile email is only used when there is no account
 * email, so a newly bound account email is never overridden silently.
 */
export function resolveDefaultEmail(
  accountEmail: string,
  profileEmail: string
): string {
  const account = accountEmail.trim()
  if (account) return account
  return profileEmail.trim()
}

/**
 * Sum the selected order amounts without floating-point drift.
 */
export function sumOrderAmounts(orders: readonly InvoiceableOrder[]): number {
  return orders.reduce((sum, order) => sum + order.amount, 0)
}

/**
 * True when the selected orders use more than one currency.
 */
export function hasMixedCurrency(
  orders: readonly InvoiceableOrder[]
): boolean {
  return new Set(orders.map((order) => order.currency)).size > 1
}

/**
 * The minimum-amount rule is enabled only for a positive configured minimum;
 * exact equality satisfies the threshold.
 */
export function isBelowMinimum(
  total: number,
  minAmount: number
): boolean {
  return minAmount > 0 && total < minAmount
}

/**
 * The invoice handling fee in the invoice currency: rate applied to the total,
 * rounded to 2 decimal places.
 */
export function calculateInvoiceFee(total: number, feeRate: number): number {
  return Math.round(total * feeRate * 100) / 100
}

/**
 * The handling fee expressed as balance quota, mirroring the backend
 * conversion (total * quotaPerUnit * rate). This drives the client-side
 * insufficient-balance gate; the server recomputes and enforces it exactly.
 */
export function calculateInvoiceFeeQuota(
  total: number,
  feeRate: number,
  quotaPerUnit: number
): number {
  return Math.round(total * quotaPerUnit * feeRate)
}

/**
 * Whether the user is allowed to submit: at least one order, no mixed
 * currency, not below the minimum, an account email is available, and the
 * balance is enough to cover the handling fee.
 */
export function canSubmitInvoice({
  selectedCount,
  mixedCurrency,
  belowMinimum,
  accountEmailUnavailable,
  insufficientBalance,
  submitting,
}: {
  selectedCount: number
  mixedCurrency: boolean
  belowMinimum: boolean
  accountEmailUnavailable: boolean
  insufficientBalance: boolean
  submitting: boolean
}): boolean {
  if (submitting) return false
  if (selectedCount === 0) return false
  if (mixedCurrency) return false
  if (belowMinimum) return false
  if (accountEmailUnavailable) return false
  if (insufficientBalance) return false
  return true
}

/**
 * Whether an invoice reason is required for the given type.
 */
export function reasonRequired(invoiceType: InvoiceType): boolean {
  return invoiceType === 'individual'
}
