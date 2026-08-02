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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatPaymentAmount } from '@/lib/currency'
import { formatNumber, formatTimestampToDate } from '@/lib/format'

import { getInvoiceDetail } from '../api'
import { getInvoiceStatusConfig } from '../lib/status'
import type { InvoiceDetail } from '../types'

interface InvoiceDetailDialogProps {
  invoiceId: number | null
  open: boolean
  onOpenChange: (open: boolean) => void
}
function formatInvoiceAmount(amount: number, currency: string): string {
  return (
    formatPaymentAmount(amount, currency, {
      digitsLarge: 2,
      digitsSmall: 2,
      abbreviate: false,
    }) ?? formatNumber(amount)
  )
}

export function InvoiceDetailDialog({
  invoiceId,
  open,
  onOpenChange,
}: InvoiceDetailDialogProps) {
  const { t } = useTranslation()
  const [detail, setDetail] = useState<InvoiceDetail | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open || invoiceId === null) return

    let cancelled = false
    const requestedInvoiceId = invoiceId
    setLoading(true)
    setDetail(null)

    async function loadDetail() {
      try {
        const response = await getInvoiceDetail(requestedInvoiceId)
        if (cancelled) return
        if (response.success && response.data) {
          setDetail(response.data)
        } else {
          toast.error(response.message || t('Failed to load invoice details'))
        }
      } catch (error) {
        if (cancelled) return
        // eslint-disable-next-line no-console
        console.error('Failed to load invoice details:', error)
        toast.error(t('Failed to load invoice details'))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    void loadDetail()

    return () => {
      cancelled = true
    }
  }, [open, invoiceId, t])

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        detail
          ? t('Invoice Application #{{id}}', { id: detail.id })
          : t('Invoice Details')
      }
      description={t('View the details of this invoice application')}
      contentClassName='flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden max-sm:w-screen max-sm:max-w-none max-sm:rounded-none max-sm:p-4 sm:max-w-3xl'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      {loading || !detail ? (
        <div className='space-y-3'>
          <Skeleton className='h-8 w-48' />
          <Skeleton className='h-24 w-full' />
          <Skeleton className='h-32 w-full' />
        </div>
      ) : (
        <>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <StatusBadge
              label={t(getInvoiceStatusConfig(detail.status).labelKey)}
              variant={getInvoiceStatusConfig(detail.status).variant}
              copyable={false}
            />
            <div className='text-sm font-semibold'>
              {formatInvoiceAmount(detail.total_amount, detail.currency)}
            </div>
          </div>

          <div className='grid gap-3 sm:grid-cols-2'>
            <DetailField label={t('Invoice Title')} value={detail.title} />
            <DetailField label={t('Tax ID')} value={detail.tax_id} />
            <DetailField label={t('Phone')} value={detail.phone} />
            <DetailField label={t('Address')} value={detail.address} />
            <DetailField label={t('Bank Name')} value={detail.bank_name} />
            <DetailField label={t('Bank Account')} value={detail.bank_account} />
            <DetailField label={t('Invoice Email')} value={detail.email} />
            <DetailField
              label={t('Created At')}
              value={formatTimestampToDate(detail.create_time)}
            />
          </div>

          <DetailField label={t('Invoice Reason')} value={detail.reason} />
          <DetailField label={t('Remark')} value={detail.remark} />
          <DetailField label={t('Admin Note')} value={detail.admin_note} />

          <div className='space-y-2'>
            <Label className='text-muted-foreground text-xs font-medium tracking-wider uppercase'>
              {t('Related Orders')}
            </Label>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Order Number')}</TableHead>
                  <TableHead>{t('Amount')}</TableHead>
                  <TableHead>{t('Payment Method')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {detail.items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>{item.trade_no}</TableCell>
                    <TableCell>
                      {formatInvoiceAmount(item.amount, item.currency)}
                    </TableCell>
                    <TableCell>{item.payment_method}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </>
      )}
    </Dialog>
  )
}

interface DetailFieldProps {
  label: string
  value: string
}

function DetailField({ label, value }: DetailFieldProps) {
  return (
    <div className='space-y-1'>
      <Label className='text-muted-foreground text-xs'>{label}</Label>
      <div className='text-sm font-medium break-words'>{value || '-'}</div>
    </div>
  )
}

