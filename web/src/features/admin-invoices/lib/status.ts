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
import type { StatusBadgeProps } from '@/components/status-badge'

import type { InvoiceStatus } from '../types'

// ============================================================================
// Invoice Status Badge Configuration
// ============================================================================

export interface InvoiceStatusConfig {
  variant: NonNullable<StatusBadgeProps['variant']>
  labelKey: string
}

export const INVOICE_STATUS_OPTIONS: InvoiceStatus[] = [
  'pending',
  'approved',
  'issuing',
  'issued',
  'rejected',
  'cancelled',
]

export const INVOICE_STATUS_CONFIG: Record<InvoiceStatus, InvoiceStatusConfig> =
  {
    pending: { variant: 'warning', labelKey: 'Invoice status pending' },
    approved: { variant: 'info', labelKey: 'Invoice status approved' },
    issuing: { variant: 'warning', labelKey: 'Invoice status issuing' },
    issued: { variant: 'success', labelKey: 'Invoice status issued' },
    rejected: { variant: 'danger', labelKey: 'Invoice status rejected' },
    // StatusBadge has no 'default' variant; 'neutral' renders the grey badge.
    cancelled: { variant: 'neutral', labelKey: 'Invoice status cancelled' },
  }

export function getInvoiceStatusConfig(
  status: InvoiceStatus
): InvoiceStatusConfig {
  return INVOICE_STATUS_CONFIG[status] ?? INVOICE_STATUS_CONFIG.pending
}