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
import { api } from '@/lib/api'

import type {
  ApiResponse,
  InvoiceAction,
  InvoiceDetail,
  InvoiceListData,
} from './types'

// ============================================================================
// Admin Invoice API Functions
// ============================================================================

export interface AdminInvoiceListParams {
  page: number
  page_size: number
  keyword?: string
  status?: string
}

/**
 * List invoice applications with keyword/status filtering and pagination.
 */
export async function getAdminInvoices(
  params: AdminInvoiceListParams
): Promise<ApiResponse<InvoiceListData>> {
  const searchParams = new URLSearchParams({
    p: params.page.toString(),
    page_size: params.page_size.toString(),
  })
  if (params.keyword) {
    searchParams.append('keyword', params.keyword)
  }
  if (params.status) {
    searchParams.append('status', params.status)
  }
  const res = await api.get(`/api/invoice/admin?${searchParams.toString()}`)
  return res.data
}

/**
 * Get full details of an invoice application, including attached orders.
 */
export async function getAdminInvoiceDetail(
  id: number
): Promise<ApiResponse<InvoiceDetail>> {
  const res = await api.get(`/api/invoice/admin/${id}`)
  return res.data
}

/**
 * Apply an admin state transition to an invoice application.
 * The note is optional except for rejection, where the backend requires it.
 */
export async function updateInvoiceStatus(
  id: number,
  action: InvoiceAction,
  note: string
): Promise<ApiResponse> {
  const res = await api.post(`/api/invoice/admin/${id}/${action}`, { note })
  return res.data
}