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
// ============================================================================
// Admin Invoice Type Definitions
// ============================================================================

/**
 * Generic API response shape used by the invoice admin endpoints.
 */
export interface ApiResponse<T = unknown> {
  success: boolean
  message: string
  data?: T
}

export type InvoiceStatus =
  | 'pending'
  | 'approved'
  | 'issuing'
  | 'issued'
  | 'rejected'
  | 'cancelled'

export type InvoiceType = 'individual' | 'organization'

/**
 * Invoice application summary returned by the admin list endpoint.
 * This DTO intentionally excludes all invoice material (tax ID, bank
 * account, address, phone, email, reason, remark, admin note).
 */
export interface AdminInvoiceListItem {
  id: number
  user_id: number
  invoice_type: InvoiceType
  title: string
  status: InvoiceStatus
  total_amount: number
  currency: string
  create_time: number
  update_time: number
}

/**
 * Paid order snapshot attached to an invoice application.
 */
export interface InvoiceItem {
  id: number
  invoice_id: number
  order_type: string
  order_id: number
  trade_no: string
  amount: number
  currency: string
  payment_method: string
}

/**
 * Full invoice application including its attached orders (admin detail only).
 */
export interface InvoiceDetail {
  id: number
  user_id: number
  invoice_type: InvoiceType
  title: string
  tax_id: string
  phone: string
  address: string
  bank_name: string
  bank_account: string
  email: string
  reason: string
  remark: string
  status: InvoiceStatus
  admin_note: string
  total_amount: number
  currency: string
  create_time: number
  update_time: number
  items: InvoiceItem[]
}

/**
 * Paginated invoice list returned by GET /api/invoice/admin.
 */
export interface InvoiceListData {
  items: AdminInvoiceListItem[]
  total: number
}

/**
 * Admin state transitions available for an invoice application.
 */
export type InvoiceAction =
  | 'approve'
  | 'start-issue'
  | 'complete-issue'
  | 'reject'
