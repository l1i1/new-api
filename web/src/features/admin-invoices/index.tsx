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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'

import { AdminInvoiceDetailDialog } from './components/admin-invoice-detail-dialog'
import { AdminInvoicesTable } from './components/admin-invoices-table'
import type { AdminInvoiceListItem } from './types'

export function AdminInvoices() {
  const { t } = useTranslation()
  const [refreshTrigger, setRefreshTrigger] = useState(0)
  const [selectedInvoice, setSelectedInvoice] =
    useState<AdminInvoiceListItem | null>(null)

  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Invoice Management')}</SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <AdminInvoicesTable
            refreshTrigger={refreshTrigger}
            onViewDetail={setSelectedInvoice}
          />
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <AdminInvoiceDetailDialog
        invoice={selectedInvoice}
        onOpenChange={(open) => !open && setSelectedInvoice(null)}
        onSuccess={() => setRefreshTrigger((prev) => prev + 1)}
      />
    </>
  )
}