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
  CancelInvoiceResponse,
  InvoiceCreateRequest,
  InvoiceCreateResponse,
  InvoiceDetailResponse,
  InvoiceOptionsResponse,
  InvoiceRecordsResponse,
} from './types'

// ============================================================================
// Invoice API Functions
// ============================================================================

/**
 * Get invoice options: feature switch, notice, minimum amount and the user's
 * currently invoiceable orders.
 */
export async function getInvoiceOptions(): Promise<InvoiceOptionsResponse> {
  const res = await api.get('/api/invoice/options', {
    skipBusinessError: true,
  })
  return res.data
}

/**
 * Create an invoice application for the selected orders.
 */
export async function createInvoice(
  request: InvoiceCreateRequest
): Promise<InvoiceCreateResponse> {
  const res = await api.post('/api/invoice', request, {
    skipBusinessError: true,
  })
  return res.data
}

/**
 * Get the current user's invoice applications (paginated).
 */
export async function getUserInvoices(
  page: number,
  pageSize: number
): Promise<InvoiceRecordsResponse> {
  const params = new URLSearchParams({
    p: page.toString(),
    page_size: pageSize.toString(),
  })
  const res = await api.get(`/api/invoice?${params.toString()}`, {
    skipBusinessError: true,
  })
  return res.data
}

/**
 * Get a single invoice application with its attached orders.
 */
export async function getInvoiceDetail(
  id: number
): Promise<InvoiceDetailResponse> {
  const res = await api.get(`/api/invoice/${id}`, {
    skipBusinessError: true,
  })
  return res.data
}

/**
 * Cancel a pending invoice application.
 */
export async function cancelInvoice(
  id: number
): Promise<CancelInvoiceResponse> {
  const res = await api.post(`/api/invoice/${id}/cancel`, undefined, {
    skipBusinessError: true,
  })
  return res.data
}
