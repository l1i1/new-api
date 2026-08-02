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
// Invoice Status Configuration
// ============================================================================

export interface InvoiceStatusConfig {
  variant: StatusBadgeProps['variant']
  /** i18n key rendered through t() at the call site. */
  labelKey: string
}

/**
 * Status badge configuration for the six invoice application states. The
 * variant comes from the StatusBadge palette; the label is the English source
 * key that i18n:sync picks up automatically.
 */
export const INVOICE_STATUS_CONFIG: Record<InvoiceStatus, InvoiceStatusConfig> =
  {
    pending: { variant: 'warning', labelKey: 'Invoice status pending' },
    approved: { variant: 'info', labelKey: 'Invoice status approved' },
    issuing: { variant: 'warning', labelKey: 'Invoice status issuing' },
    issued: { variant: 'success', labelKey: 'Invoice status issued' },
    rejected: { variant: 'danger', labelKey: 'Invoice status rejected' },
    cancelled: { variant: 'neutral', labelKey: 'Invoice status cancelled' },
  }

/**
 * Get the badge configuration for a status, falling back to pending for
 * unknown statuses so the UI never crashes on a future backend value.
 */
export function getInvoiceStatusConfig(
  status: InvoiceStatus
): InvoiceStatusConfig {
  return INVOICE_STATUS_CONFIG[status] ?? INVOICE_STATUS_CONFIG.pending
}
