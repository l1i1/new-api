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
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StatusBadge } from '@/components/status-badge'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
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

import { cancelInvoice, getUserInvoices } from '../api'
import { getInvoiceStatusConfig } from '../lib/status'
import type { Invoice } from '../types'
import { InvoiceDetailDialog } from './invoice-detail-dialog'

const PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const

interface InvoiceRecordsPanelProps {
  refreshKey?: number
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

export function InvoiceRecordsPanel({
  refreshKey = 0,
}: InvoiceRecordsPanelProps) {
  const { t } = useTranslation()
  const [records, setRecords] = useState<Invoice[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(10)
  const [loading, setLoading] = useState(true)
  const [cancellingId, setCancellingId] = useState<number | null>(null)
  const [cancelling, setCancelling] = useState(false)
  const [detailId, setDetailId] = useState<number | null>(null)

  const fetchRecords = useCallback(async () => {
    try {
      setLoading(true)
      const response = await getUserInvoices(page, pageSize)
      if (response.success && response.data) {
        setRecords(response.data.items || [])
        setTotal(response.data.total || 0)
      } else {
        toast.error(response.message || t('Failed to load invoice records'))
        setRecords([])
        setTotal(0)
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to load invoice records:', error)
      toast.error(t('Failed to load invoice records'))
      setRecords([])
      setTotal(0)
    } finally {
      setLoading(false)
    }
  }, [page, pageSize, t])

  useEffect(() => {
    void fetchRecords()
  }, [fetchRecords, refreshKey])

  const handlePageChange = useCallback((newPage: number) => {
    setPage(newPage)
  }, [])

  const handlePageSizeChange = useCallback((newPageSize: number) => {
    setPageSize(newPageSize)
    setPage(1)
  }, [])

  const handleConfirmCancel = useCallback(async () => {
    if (cancellingId === null) return

    setCancelling(true)
    try {
      const response = await cancelInvoice(cancellingId)
      if (response.success) {
        toast.success(t('Invoice application cancelled successfully'))
        setCancellingId(null)
        await fetchRecords()
      } else {
        toast.error(
          response.message || t('Failed to cancel invoice application')
        )
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to cancel invoice application:', error)
      toast.error(t('Failed to cancel invoice application'))
    } finally {
      setCancelling(false)
    }
  }, [cancellingId, fetchRecords, t])

  const totalPages = Math.ceil(total / pageSize)

  let recordsContent: ReactNode
  if (loading) {
    recordsContent = (
      <div className='space-y-2'>
        <Skeleton className='h-12 w-full' />
        <Skeleton className='h-12 w-full' />
        <Skeleton className='h-12 w-full' />
      </div>
    )
  } else if (records.length === 0) {
    recordsContent = (
      <div className='py-10 text-center'>
        <p className='text-muted-foreground text-sm'>
          {t('No invoice records yet')}
        </p>
      </div>
    )
  } else {
    recordsContent = (
      <InvoiceRecordsTable
        records={records}
        onViewDetail={setDetailId}
        onCancel={setCancellingId}
      />
    )
  }

  return (
    <div className='space-y-3'>
      {recordsContent}

      {!loading && records.length > 0 && (
        <InvoiceRecordsPagination
          page={page}
          pageSize={pageSize}
          total={total}
          totalPages={totalPages}
          onPageChange={handlePageChange}
          onPageSizeChange={handlePageSizeChange}
        />
      )}

      <InvoiceDetailDialog
        invoiceId={detailId}
        open={detailId !== null}
        onOpenChange={(open) => {
          if (!open) setDetailId(null)
        }}
      />

      <AlertDialog
        open={cancellingId !== null}
        onOpenChange={(open) => {
          if (!open) setCancellingId(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('Cancel Invoice Application')}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                'Are you sure you want to cancel this invoice application? The selected orders will become available for a new application.'
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={cancelling}>
              {t('Cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={() => void handleConfirmCancel()}
              disabled={cancelling}
            >
              {cancelling ? t('Processing...') : t('Confirm')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

interface InvoiceRecordsTableProps {
  records: Invoice[]
  onViewDetail: (invoiceId: number) => void
  onCancel: (invoiceId: number) => void
}

function InvoiceRecordsTable({
  records,
  onViewDetail,
  onCancel,
}: InvoiceRecordsTableProps) {
  const { t } = useTranslation()

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('Application ID')}</TableHead>
          <TableHead>{t('Amount')}</TableHead>
          <TableHead>{t('Status')}</TableHead>
          <TableHead>{t('Created At')}</TableHead>
          <TableHead>{t('Actions')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {records.map((invoice) => {
          const statusConfig = getInvoiceStatusConfig(invoice.status)
          return (
            <TableRow key={invoice.id}>
              <TableCell className='font-medium'>{invoice.id}</TableCell>
              <TableCell>
                {formatInvoiceAmount(invoice.total_amount, invoice.currency)}
              </TableCell>
              <TableCell>
                <StatusBadge
                  label={t(statusConfig.labelKey)}
                  variant={statusConfig.variant}
                  copyable={false}
                />
              </TableCell>
              <TableCell>
                {formatTimestampToDate(invoice.create_time)}
              </TableCell>
              <TableCell>
                <div className='flex items-center gap-2'>
                  <Button
                    variant='outline'
                    size='sm'
                    onClick={() => onViewDetail(invoice.id)}
                  >
                    {t('Details')}
                  </Button>
                  {invoice.status === 'pending' && (
                    <Button
                      variant='destructive'
                      size='sm'
                      onClick={() => onCancel(invoice.id)}
                    >
                      {t('Cancel')}
                    </Button>
                  )}
                </div>
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </Table>
  )
}

interface InvoiceRecordsPaginationProps {
  page: number
  pageSize: number
  total: number
  totalPages: number
  onPageChange: (page: number) => void
  onPageSizeChange: (pageSize: number) => void
}

function InvoiceRecordsPagination({
  page,
  pageSize,
  total,
  totalPages,
  onPageChange,
  onPageSizeChange,
}: InvoiceRecordsPaginationProps) {
  const { t } = useTranslation()

  return (
    <div className='flex flex-col items-center gap-3 border-t pt-4 sm:flex-row sm:items-center sm:justify-between'>
      <div className='text-muted-foreground text-xs sm:text-sm'>
        {t('Showing')} {(page - 1) * pageSize + 1}-
        {Math.min(page * pageSize, total)} {t('of')} {total}
      </div>
      <div className='flex items-center gap-2'>
        <Button
          variant='outline'
          size='sm'
          onClick={() => onPageChange(page - 1)}
          disabled={page <= 1}
          className='h-8 w-8 p-0'
          aria-label={t('Previous page')}
        >
          <ChevronLeft className='h-4 w-4' />
        </Button>
        <div className='text-muted-foreground flex items-center gap-1 text-sm'>
          <span className='font-medium'>{page}</span>
          <span>/</span>
          <span>{totalPages}</span>
        </div>
        <Button
          variant='outline'
          size='sm'
          onClick={() => onPageChange(page + 1)}
          disabled={page >= totalPages}
          className='h-8 w-8 p-0'
          aria-label={t('Next page')}
        >
          <ChevronRight className='h-4 w-4' />
        </Button>
        <Select
          items={PAGE_SIZE_OPTIONS.map((size) => ({
            value: size.toString(),
            label: t('{{size}} / page', { size }),
          }))}
          value={pageSize.toString()}
          onValueChange={(value) =>
            value !== null && onPageSizeChange(Number.parseInt(value))
          }
        >
          <SelectTrigger className='h-8 w-[92px] sm:w-32'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <SelectGroup>
              {PAGE_SIZE_OPTIONS.map((size) => (
                <SelectItem key={size} value={size.toString()}>
                  {t('{{size}} / page', { size })}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
    </div>
  )
}
