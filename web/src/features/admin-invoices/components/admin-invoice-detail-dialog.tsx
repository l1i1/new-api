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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { formatNumber, formatTimestampToDate } from '@/lib/format'

import { getAdminInvoiceDetail, updateInvoiceStatus } from '../api'
import { getInvoiceStatusConfig } from '../lib/status'
import type {
  AdminInvoiceListItem,
  InvoiceAction,
  InvoiceDetail,
  InvoiceStatus,
} from '../types'

interface AdminInvoiceDetailDialogProps {
  invoice: AdminInvoiceListItem | null
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

const ACTION_CONFIG: Record<
  InvoiceAction,
  { titleKey: string; descriptionKey: string; noteRequired: boolean }
> = {
  approve: {
    titleKey: 'Approve Invoice',
    descriptionKey:
      'Approve this invoice application and continue to invoicing',
    noteRequired: false,
  },
  reject: {
    titleKey: 'Reject Invoice',
    descriptionKey:
      'Reject this invoice application. The rejection reason is required and will be sent to the user',
    noteRequired: true,
  },
  'start-issue': {
    titleKey: 'Start Issuing',
    descriptionKey: 'Mark this invoice as being issued',
    noteRequired: false,
  },
  'complete-issue': {
    titleKey: 'Complete Issuing',
    descriptionKey: 'Mark this invoice as issued',
    noteRequired: false,
  },
}
const ACTION_BUTTONS: Partial<
  Record<
    InvoiceStatus,
    Array<{
      action: InvoiceAction
      labelKey: string
      variant?: 'default' | 'destructive'
    }>
  >
> = {
  pending: [
    { action: 'reject', labelKey: 'Reject', variant: 'destructive' },
    { action: 'approve', labelKey: 'Approve' },
  ],
  approved: [{ action: 'start-issue', labelKey: 'Start Issuing' }],
  issuing: [{ action: 'complete-issue', labelKey: 'Complete Issuing' }],
}
function InfoField({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className='space-y-1'>
      <Label className='text-muted-foreground text-xs'>{label}</Label>
      <div className='text-sm font-medium break-all'>{value || '-'}</div>
    </div>
  )
}
function InvoiceInfoGrid({ detail }: { detail: InvoiceDetail }) {
  const { t } = useTranslation()
  const config = getInvoiceStatusConfig(detail.status)
  const fields: Array<{ labelKey: string; value: ReactNode }> = [
    {
      labelKey: 'Status',
      value: (
        <StatusBadge
          label={t(config.labelKey)}
          variant={config.variant}
          copyable={false}
        />
      ),
    },
    {
      labelKey: 'Amount',
      value: `${formatNumber(detail.total_amount)} ${detail.currency}`,
    },
    {
      labelKey: 'Invoice Type',
      value: t(detail.invoice_type === 'individual' ? 'Individual' : 'Company'),
    },
    { labelKey: 'Title', value: detail.title },
    { labelKey: 'Tax ID', value: detail.tax_id },
    { labelKey: 'Phone', value: detail.phone },
    { labelKey: 'Address', value: detail.address },
    { labelKey: 'Bank Name', value: detail.bank_name },
    { labelKey: 'Bank Account', value: detail.bank_account },
    { labelKey: 'Email', value: detail.email },
    { labelKey: 'Reason', value: detail.reason },
    { labelKey: 'User Remark', value: detail.remark },
    { labelKey: 'Admin Note', value: detail.admin_note },
    {
      labelKey: 'Created At',
      value: formatTimestampToDate(detail.create_time),
    },
    {
      labelKey: 'Updated At',
      value: formatTimestampToDate(detail.update_time),
    },
  ]
  return (
    <div className='grid grid-cols-1 gap-4 sm:grid-cols-2'>
      {fields.map((field) => (
        <InfoField
          key={field.labelKey}
          label={t(field.labelKey)}
          value={field.value}
        />
      ))}
    </div>
  )
}

export function AdminInvoiceDetailDialog({
  invoice,
  onOpenChange,
  onSuccess,
}: AdminInvoiceDetailDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [confirmAction, setConfirmAction] = useState<InvoiceAction | null>(null)
  const [note, setNote] = useState('')
  const [noteError, setNoteError] = useState<string | null>(null)
  const [pdfFile, setPdfFile] = useState<File | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const { data: detail, isLoading } = useQuery({
    queryKey: ['admin-invoice-detail', invoice?.id],
    queryFn: async () => {
      if (invoice == null) return null
      const result = await getAdminInvoiceDetail(invoice.id)
      if (!result.success) {
        toast.error(result.message || t('Failed to load invoice detail'))
        return null
      }
      return result.data ?? null
    },
    enabled: invoice !== null,
  })
  const handleActionClick = (action: InvoiceAction) => {
    setConfirmAction(action)
    setNote('')
    setNoteError(null)
    setPdfFile(null)
  }
  const handleConfirmAction = async () => {
    if (!detail || !confirmAction) return
    const config = ACTION_CONFIG[confirmAction]
    const trimmedNote = note.trim()
    if (config.noteRequired && !trimmedNote) {
      setNoteError(t('The rejection reason is required'))
      return
    }
    if (confirmAction === 'complete-issue' && !pdfFile) {
      setNoteError(t('A real invoice PDF is required before marking the application as issued'))
      return
    }
    setSubmitting(true)
    try {
      const result = await updateInvoiceStatus(
        detail.id,
        confirmAction,
        trimmedNote,
        confirmAction === 'complete-issue' ? pdfFile : null
      )
      if (!result.success) {
        toast.error(result.message || t('Failed to update invoice'))
        return
      }
      toast.success(t('Invoice updated successfully'))
      setConfirmAction(null)
      setNote('')
      setNoteError(null)
      setPdfFile(null)
      await queryClient.invalidateQueries({
        queryKey: ['admin-invoice-detail', detail.id],
      })
      onSuccess()
    } finally {
      setSubmitting(false)
    }
  }
  const actionButtons = (ACTION_BUTTONS[detail?.status ?? 'pending'] ?? []).map(
    ({ action, labelKey, variant }) => (
      <Button
        key={action}
        variant={variant ?? 'default'}
        onClick={() => handleActionClick(action)}
      >
        {t(labelKey)}
      </Button>
    )
  )
  return (
    <>
      <Dialog
        open={invoice !== null}
        onOpenChange={onOpenChange}
        title={
          invoice ? `${t('Invoice')} #${invoice.id}` : t('Invoice Details')
        }
        description={t('Review the invoice application and manage its status')}
        contentClassName='sm:max-w-3xl'
      >
        {!detail ? (
          <div className='text-muted-foreground flex h-40 items-center justify-center text-sm'>
            {isLoading
              ? t('Loading invoice details...')
              : t('Failed to load invoice detail')}
          </div>
        ) : (
          <div className='space-y-6'>
            <InvoiceInfoGrid detail={detail} />
            <div>
              <Label className='text-muted-foreground text-xs'>
                {t('Related Orders')}
              </Label>
              {detail.items.length === 0 ? (
                <div className='text-muted-foreground mt-1 text-sm'>
                  {t('No related orders')}
                </div>
              ) : (
                <div className='mt-2 space-y-2'>
                  {detail.items.map((item) => (
                    <div
                      key={item.id}
                      className='flex items-center justify-between gap-4 rounded-lg border p-3'
                    >
                      <div className='min-w-0'>
                        <div className='truncate font-medium'>
                          {item.trade_no || '-'}
                        </div>
                        <div className='text-muted-foreground text-xs'>
                          {item.payment_method || '-'}
                        </div>
                      </div>
                      <div className='font-semibold whitespace-nowrap tabular-nums'>
                        {formatNumber(item.amount)} {item.currency}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
            {actionButtons.length > 0 && (
              <div className='flex flex-wrap justify-end gap-2'>
                {actionButtons}
              </div>
            )}
          </div>
        )}
      </Dialog>
      <AlertDialog
        open={confirmAction !== null}
        onOpenChange={(isOpen) => !isOpen && setConfirmAction(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirmAction ? t(ACTION_CONFIG[confirmAction].titleKey) : ''}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirmAction
                ? t(ACTION_CONFIG[confirmAction].descriptionKey)
                : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className='space-y-2'>
            <Label className='text-muted-foreground text-xs'>
              {t('Note')}
              {confirmAction && ACTION_CONFIG[confirmAction].noteRequired
                ? ` (${t('required')})`
                : ''}
            </Label>
            <Textarea
              value={note}
              onChange={(e) => {
                setNote(e.target.value)
                if (noteError) setNoteError(null)
              }}
              placeholder={t('Enter an optional note for this action')}
            />
            {confirmAction === 'complete-issue' && (
              <div className='space-y-2'>
                <Label className='text-muted-foreground text-xs'>
                  {t('Real invoice PDF')} ({t('required')})
                </Label>
                <Input
                  type='file'
                  accept='application/pdf,.pdf'
                  onChange={(e) => {
                    setPdfFile(e.target.files?.[0] ?? null)
                    if (noteError) setNoteError(null)
                  }}
                />
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'The uploaded PDF is sent immediately with the issued notification email and is not retained by the system.'
                  )}
                </p>
              </div>
            )}
            {noteError ? (
              <p className='text-destructive text-xs'>{noteError}</p>
            ) : null}
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={submitting}>
              {t('Cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={handleConfirmAction}
              disabled={submitting}
            >
              {submitting ? t('Processing...') : t('Confirm')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
