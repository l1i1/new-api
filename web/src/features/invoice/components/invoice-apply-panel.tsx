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
import { zodResolver } from '@hookform/resolvers/zod'
import type { TFunction } from 'i18next'
import { useCallback, useEffect, useState } from 'react'
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { RichContent } from '@/components/rich-content'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { isLikelyHtml } from '@/lib/content-format'
import { formatPaymentAmount } from '@/lib/currency'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { resolveTntContent } from '@/lib/tnt-content'
import { useAuthStore } from '@/stores/auth-store'

import {
  createInvoice,
  getInvoiceOptions,
  getInvoiceProfile,
  saveInvoiceProfile,
} from '../api'
import {
  canSubmitInvoice,
  hasMixedCurrency,
  isBelowMinimum,
  resolveDefaultEmail,
  sumOrderAmounts,
} from '../lib/apply'
import type {
  InvoiceableOrder,
  InvoiceOptions,
  InvoiceProfile,
  InvoiceType,
} from '../types'

interface InvoiceApplyPanelProps {
  onSubmitted: () => void
}
interface InvoiceApplyFormValues {
  invoice_type: InvoiceType
  reason: string
  remark: string
  email: string
  title: string
  tax_id: string
  phone: string
  address: string
  bank_name: string
  bank_account: string
}

const INVOICE_APPLY_DEFAULT_VALUES: InvoiceApplyFormValues = {
  invoice_type: 'company',
  reason: '',
  remark: '',
  email: '',
  title: '',
  tax_id: '',
  phone: '',
  address: '',
  bank_name: '',
  bank_account: '',
}

function getInvoiceApplyFormSchema(t: TFunction) {
  return z
    .object({
      invoice_type: z.enum(['individual', 'company']),
      reason: z.string().trim(),
      remark: z.string().trim(),
      email: z.string().trim().email(t('Please enter a valid email address')),
      title: z.string().trim().min(1, t('Invoice title is required')),
      tax_id: z.string().trim().min(1, t('Tax ID is required')),
      phone: z.string().trim(),
      address: z.string().trim(),
      bank_name: z.string().trim(),
      bank_account: z.string().trim(),
    })
    .superRefine((values, context) => {
      if (values.invoice_type === 'individual' && values.reason === '') {
        context.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['reason'],
          message: t('Individual invoice reason is required'),
        })
      }
    })
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

export function InvoiceApplyPanel({ onSubmitted }: InvoiceApplyPanelProps) {
  const { t } = useTranslation()
  const authEmail = useAuthStore(
    (state) => state.auth.user?.email?.trim() ?? ''
  )
  const [options, setOptions] = useState<InvoiceOptions | null>(null)
  const [profile, setProfile] = useState<InvoiceProfile | null>(null)
  const [loading, setLoading] = useState(true)
  const [selectedIds, setSelectedIds] = useState<ReadonlySet<number>>(
    new Set<number>()
  )
  const [submitting, setSubmitting] = useState(false)

  const fetchOptions = useCallback(async () => {
    try {
      setLoading(true)
      const response = await getInvoiceOptions()
      if (response.success && response.data) {
        setOptions(response.data)
      } else {
        toast.error(response.message || t('Failed to load invoice options'))
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to load invoice options:', error)
      toast.error(t('Failed to load invoice options'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    void fetchOptions()
  }, [fetchOptions])

  const fetchProfile = useCallback(async () => {
    try {
      const response = await getInvoiceProfile()
      if (response.success && response.data) {
        setProfile(response.data)
      } else if (response.message) {
        toast.error(response.message)
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to load invoice profile:', error)
      toast.error(t('Failed to load invoice information'))
    }
  }, [t])

  useEffect(() => {
    void fetchProfile()
  }, [fetchProfile])

  const orders = options?.orders ?? []
  const selectedOrders = orders.filter((order) =>
    selectedIds.has(order.order_id)
  )

  const toggleOrder = useCallback((orderId: number) => {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (next.has(orderId)) {
        next.delete(orderId)
      } else {
        next.add(orderId)
      }
      return next
    })
  }, [])

  const allPageSelected =
    orders.length > 0 &&
    orders.every((order) => selectedIds.has(order.order_id))
  const somePageSelected = orders.some((order) =>
    selectedIds.has(order.order_id)
  )

  const toggleAllOrders = () => {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (allPageSelected) {
        orders.forEach((order) => next.delete(order.order_id))
      } else {
        orders.forEach((order) => next.add(order.order_id))
      }
      return next
    })
  }

  const mixedCurrency = hasMixedCurrency(selectedOrders)
  const selectedCurrency = selectedOrders[0]?.currency ?? ''
  const selectedTotal = sumOrderAmounts(selectedOrders)
  const minAmount = options?.min_amount ?? 0
  const belowMinAmount = isBelowMinimum(selectedTotal, minAmount)
  const accountEmailUnavailable = !authEmail
  const submitDisabled = !canSubmitInvoice({
    selectedCount: selectedOrders.length,
    mixedCurrency,
    belowMinimum: belowMinAmount,
    accountEmailUnavailable,
    submitting,
  })

  async function onSubmit(values: InvoiceApplyFormValues) {
    if (selectedOrders.length === 0) return

    setSubmitting(true)
    try {
      const response = await createInvoice({
        orders: selectedOrders.map((order) => ({
          order_type: order.order_type,
          order_id: order.order_id,
        })),
        invoice_type: values.invoice_type,
        title: values.title,
        tax_id: values.tax_id,
        phone: values.phone,
        address: values.address,
        bank_name: values.bank_name,
        bank_account: values.bank_account,
        email: values.email,
        reason: values.reason,
        remark: values.remark,
      })
      if (response.success && response.data) {
        toast.success(t('Invoice application submitted successfully'))
        try {
          const profileResponse = await saveInvoiceProfile({
            invoice_type: values.invoice_type,
            title: values.title,
            tax_id: values.tax_id,
            phone: values.phone,
            address: values.address,
            bank_name: values.bank_name,
            bank_account: values.bank_account,
            email: values.email,
          })
          if (profileResponse.success && profileResponse.data) {
            setProfile(profileResponse.data)
          } else {
            toast.error(
              profileResponse.message || t('Failed to save invoice information')
            )
          }
        } catch (error) {
          // eslint-disable-next-line no-console
          console.error('Failed to save invoice information:', error)
          toast.error(t('Failed to save invoice information'))
        }
        setSelectedIds(new Set<number>())
        onSubmitted()
      } else {
        toast.error(
          response.message || t('Failed to submit invoice application')
        )
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to submit invoice application:', error)
      toast.error(t('Failed to submit invoice application'))
    } finally {
      setSubmitting(false)
    }
  }

  if (loading) {
    return (
      <div className='space-y-4'>
        <Skeleton className='h-16 w-full' />
        <Skeleton className='h-64 w-full' />
      </div>
    )
  }

  if (!options || !options.enabled) {
    return (
      <div className='space-y-4'>
        {options?.notice ? <InvoiceNotice notice={options.notice} /> : null}
        <Alert>
          <AlertDescription>
            {t('The invoice feature is not enabled yet')}
          </AlertDescription>
        </Alert>
      </div>
    )
  }

  return (
    <div className='flex flex-col gap-4 sm:gap-5'>
      {options.notice ? <InvoiceNotice notice={options.notice} /> : null}

      <InvoiceOrderSelectTable
        orders={orders}
        selectedIds={selectedIds}
        allPageSelected={allPageSelected}
        somePageSelected={somePageSelected}
        onToggleAll={toggleAllOrders}
        onToggleOrder={toggleOrder}
      />

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <span className='text-muted-foreground text-sm'>
          {t('Selected {{count}} orders', { count: selectedOrders.length })}
        </span>
        <span className='text-sm font-semibold'>
          {t('Total')}:{' '}
          {mixedCurrency
            ? formatNumber(selectedTotal)
            : formatInvoiceAmount(selectedTotal, selectedCurrency)}
        </span>
      </div>

      {mixedCurrency && (
        <Alert variant='destructive'>
          <AlertDescription>
            {t(
              'Currencies are inconsistent. Please apply for invoicing by currency separately.'
            )}
          </AlertDescription>
        </Alert>
      )}

      {!mixedCurrency && belowMinAmount && (
        <Alert variant='destructive'>
          <AlertDescription>
            {t(
              'The selected amount is below the minimum invoice amount of {{amount}}',
              {
                amount: formatInvoiceAmount(minAmount, selectedCurrency),
              }
            )}
          </AlertDescription>
        </Alert>
      )}

      <InvoiceApplyForm
        accountEmail={authEmail}
        initialProfile={profile}
        submitting={submitting}
        submitDisabled={submitDisabled}
        onSubmit={onSubmit}
      />
    </div>
  )
}

export function InvoiceNotice({ notice }: { notice: string }) {
  const { i18n } = useTranslation()
  const content = resolveTntContent(notice, i18n.language).trim()
  const contentIsHtml = isLikelyHtml(content)

  return (
    <Alert>
      <AlertDescription>
        <RichContent
          content={content}
          mode={contentIsHtml ? 'html' : 'markdown'}
          breaks
          className='prose-sm dark:prose-invert'
        />
      </AlertDescription>
    </Alert>
  )
}

interface InvoiceOrderSelectTableProps {
  orders: InvoiceableOrder[]
  selectedIds: ReadonlySet<number>
  allPageSelected: boolean
  somePageSelected: boolean
  onToggleAll: () => void
  onToggleOrder: (orderId: number) => void
}

function InvoiceOrderSelectTable({
  orders,
  selectedIds,
  allPageSelected,
  somePageSelected,
  onToggleAll,
  onToggleOrder,
}: InvoiceOrderSelectTableProps) {
  const { t } = useTranslation()

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead className='w-10'>
            <Checkbox
              checked={allPageSelected}
              indeterminate={somePageSelected && !allPageSelected}
              onCheckedChange={onToggleAll}
              aria-label={t('Select all orders')}
            />
          </TableHead>
          <TableHead>{t('Order Number')}</TableHead>
          <TableHead>{t('Amount')}</TableHead>
          <TableHead>{t('Payment Method')}</TableHead>
          <TableHead>{t('Time')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {orders.length === 0 ? (
          <TableRow>
            <TableCell
              colSpan={5}
              className='text-muted-foreground text-center'
            >
              {t('No invoiceable orders yet')}
            </TableCell>
          </TableRow>
        ) : (
          orders.map((order) => (
            <TableRow key={order.order_id}>
              <TableCell>
                <Checkbox
                  checked={selectedIds.has(order.order_id)}
                  onCheckedChange={() => onToggleOrder(order.order_id)}
                  aria-label={t('Select order {{tradeNo}}', {
                    tradeNo: order.trade_no,
                  })}
                />
              </TableCell>
              <TableCell>{order.trade_no}</TableCell>
              <TableCell>
                {formatInvoiceAmount(order.amount, order.currency)}
              </TableCell>
              <TableCell>{order.payment_method}</TableCell>
              <TableCell>{formatTimestampToDate(order.create_time)}</TableCell>
            </TableRow>
          ))
        )}
      </TableBody>
    </Table>
  )
}

interface InvoiceApplyFormProps {
  accountEmail: string
  initialProfile: InvoiceProfile | null
  submitting: boolean
  submitDisabled: boolean
  onSubmit: (values: InvoiceApplyFormValues) => void
}

export function InvoiceApplyForm({
  accountEmail,
  initialProfile,
  submitting,
  submitDisabled,
  onSubmit,
}: InvoiceApplyFormProps) {
  const { t } = useTranslation()
  const schema = getInvoiceApplyFormSchema(t)
  const [savingProfile, setSavingProfile] = useState(false)
  const form = useForm<InvoiceApplyFormValues>({
    resolver: zodResolver(
      schema
    ) as unknown as Resolver<InvoiceApplyFormValues>,
    defaultValues: {
      ...INVOICE_APPLY_DEFAULT_VALUES,
      email: accountEmail,
    },
  })
  const invoiceType = form.watch('invoice_type')

  useEffect(() => {
    if (!initialProfile && !accountEmail) return
    if (form.formState.isDirty) return

    const currentValues = form.getValues()
    form.reset({
      ...INVOICE_APPLY_DEFAULT_VALUES,
      ...initialProfile,
      invoice_type: initialProfile?.invoice_type || 'company',
      // The account email is the current default; a historically saved
      // delivery email is only used as a fallback when there is no account
      // email, so a newly bound account email is never overridden silently.
      email: resolveDefaultEmail(accountEmail, initialProfile?.email ?? ''),
      reason: currentValues.reason,
      remark: currentValues.remark,
    })
  }, [accountEmail, form, initialProfile])

  async function handleSaveProfile() {
    const valid = await form.trigger([
      'invoice_type',
      'title',
      'tax_id',
      'email',
    ])
    if (!valid) return

    const values = form.getValues()
    setSavingProfile(true)
    try {
      const response = await saveInvoiceProfile({
        invoice_type: values.invoice_type,
        title: values.title,
        tax_id: values.tax_id,
        phone: values.phone,
        address: values.address,
        bank_name: values.bank_name,
        bank_account: values.bank_account,
        email: values.email,
      })
      if (response.success) {
        toast.success(t('Invoice information saved successfully'))
      } else {
        toast.error(response.message || t('Failed to save invoice information'))
      }
    } catch (error) {
      // eslint-disable-next-line no-console
      console.error('Failed to save invoice information:', error)
      toast.error(t('Failed to save invoice information'))
    } finally {
      setSavingProfile(false)
    }
  }

  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit(onSubmit)}
        className='space-y-4'
        autoComplete='off'
      >
        <FormField
          control={form.control}
          name='invoice_type'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Invoice Type')}</FormLabel>
              <FormControl>
                <RadioGroup
                  value={field.value}
                  onValueChange={(value) => {
                    field.onChange(value as InvoiceType)
                    form.clearErrors('reason')
                  }}
                  className='flex flex-wrap gap-4'
                >
                  <div className='flex items-center gap-2'>
                    <RadioGroupItem
                      id='invoice-type-company'
                      value='company'
                      disabled={submitting || savingProfile}
                    />
                    <FormLabel
                      htmlFor='invoice-type-company'
                      className='font-normal'
                    >
                      {t('Company')}
                    </FormLabel>
                  </div>
                  <div className='flex items-center gap-2'>
                    <RadioGroupItem
                      id='invoice-type-individual'
                      value='individual'
                      disabled={submitting || savingProfile}
                    />
                    <FormLabel
                      htmlFor='invoice-type-individual'
                      className='font-normal'
                    >
                      {t('Individual')}
                    </FormLabel>
                  </div>
                </RadioGroup>
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <div className='grid gap-4 sm:grid-cols-2'>
          <FormField
            control={form.control}
            name='title'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Invoice Title')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={
                      invoiceType === 'individual'
                        ? t('Individual name')
                        : t('Company name')
                    }
                    disabled={submitting || savingProfile}
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='tax_id'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Tax ID')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('Tax registration number')}
                    disabled={submitting || savingProfile}
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='phone'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Phone')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('Contact phone (optional)')}
                    disabled={submitting || savingProfile}
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='address'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Address')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('Billing address (optional)')}
                    disabled={submitting || savingProfile}
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='bank_name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Bank Name')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('Opening bank (optional)')}
                    disabled={submitting || savingProfile}
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='bank_account'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Bank Account')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder={t('Bank account number (optional)')}
                    disabled={submitting || savingProfile}
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </div>

        <FormField
          control={form.control}
          name='reason'
          render={({ field }) => (
            <FormItem>
              <FormLabel>
                {t('Invoice Reason')}
                {invoiceType === 'individual' ? ` (${t('required')})` : ''}
              </FormLabel>
              <FormControl>
                <Textarea
                  placeholder={t('Why do you need this invoice?')}
                  disabled={submitting || savingProfile}
                  rows={3}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={form.control}
          name='remark'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Remark')}</FormLabel>
              <FormControl>
                <Textarea
                  placeholder={t('Anything we should know (optional)')}
                  disabled={submitting || savingProfile}
                  rows={2}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={form.control}
          name='email'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Invoice Email')}</FormLabel>
              <FormControl>
                <Input
                  type='email'
                  placeholder={accountEmail || t('Email Address')}
                  disabled={submitting || savingProfile}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <div className='flex flex-wrap justify-end gap-2'>
          <Button
            type='button'
            variant='outline'
            disabled={submitting || savingProfile}
            onClick={() => void handleSaveProfile()}
          >
            {savingProfile ? t('Saving...') : t('Save Invoice Information')}
          </Button>
          <Button type='submit' disabled={submitDisabled || savingProfile}>
            {submitting ? t('Submitting...') : t('Submit Invoice Application')}
          </Button>
        </div>
      </form>
    </Form>
  )
}
