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
// Invoice Types
// ============================================================================

export type InvoiceOrderType = 'topup'

export type InvoiceType = 'individual' | 'company'

export type InvoiceStatus =
  | 'pending'
  | 'approved'
  | 'issuing'
  | 'issued'
  | 'rejected'
  | 'cancelled'

export interface ApiResponse<T = unknown> {
  success: boolean
  message: string
  data?: T
}

/** A paid top-up order that can be attached to an invoice application. */
export interface InvoiceableOrder {
  order_type: InvoiceOrderType
  order_id: number
  trade_no: string
  amount: number
  currency: string
  payment_method: string
  create_time: number
}

/** Invoice feature options served by GET /api/user/invoice/options. */
export interface InvoiceOptions {
  enabled: boolean
  notice: string
  min_amount: number
  orders: InvoiceableOrder[]
}

/** An invoice application (list and detail share this base shape). */
export interface Invoice {
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
}

/** Snapshot of one order attached to an invoice application. */
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

/** Invoice detail with its attached order snapshots. */
export interface InvoiceDetail extends Invoice {
  items: InvoiceItem[]
}

/** Reference to a selected order inside an invoice create request. */
export interface InvoiceOrderRef {
  order_type: InvoiceOrderType
  order_id: number
}

/** Reusable billing information saved for the current user. */
export interface InvoiceProfile {
  invoice_type: InvoiceType
  title: string
  tax_id: string
  phone: string
  address: string
  bank_name: string
  bank_account: string
  email: string
}

/** Body of POST /api/user/invoice. */
export interface InvoiceCreateRequest {
  orders: InvoiceOrderRef[]
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
}

/** Paginated invoice record list data. */
export interface InvoiceRecordsData {
  items: Invoice[]
  total: number
}

export type InvoiceOptionsResponse = ApiResponse<InvoiceOptions>
export type InvoiceProfileResponse = ApiResponse<InvoiceProfile>
export type InvoiceCreateResponse = ApiResponse<Invoice>
export type InvoiceRecordsResponse = ApiResponse<InvoiceRecordsData>
export type InvoiceDetailResponse = ApiResponse<InvoiceDetail>
export type CancelInvoiceResponse = ApiResponse<null>
