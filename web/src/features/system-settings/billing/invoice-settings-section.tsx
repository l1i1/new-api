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
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { MultiSelect } from '@/components/multi-select'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  getPaymentMethodOptions,
  normalizePaymentMethodValues,
} from './invoice-payment-methods'

const schema = z.object({
  enabled: z.boolean(),
  notice: z.string(),
  minAmount: z.coerce.number().min(0),
  allowedPaymentMethods: z.array(z.string()),
})

type Values = z.infer<typeof schema>

export function InvoiceSettingsSection({
  defaultValues,
}: {
  defaultValues: {
    enabled: boolean
    notice: string
    minAmount: number
    allowedPaymentMethods: string[]
    paymentMethodConfig: string
  }
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const form = useForm<Values>({
    resolver: zodResolver(schema) as unknown as Resolver<Values>,
    defaultValues: {
      enabled: defaultValues.enabled,
      notice: defaultValues.notice,
      minAmount: defaultValues.minAmount,
      allowedPaymentMethods: normalizePaymentMethodValues(
        defaultValues.allowedPaymentMethods
      ),
    },
  })

  const { isDirty, isSubmitting } = form.formState
  const enabled = form.watch('enabled')
  const allowedPaymentMethods = form.watch('allowedPaymentMethods')
  const paymentMethodOptions = getPaymentMethodOptions(
    defaultValues.paymentMethodConfig,
    allowedPaymentMethods,
    t
  )

  async function onSubmit(values: Values) {
    const updates: Array<{ key: string; value: string }> = []

    if (values.enabled !== defaultValues.enabled) {
      updates.push({
        key: 'InvoiceEnabled',
        value: String(values.enabled),
      })
    }

    if (values.notice !== defaultValues.notice) {
      updates.push({
        key: 'InvoiceNotice',
        value: values.notice,
      })
    }

    if (values.minAmount !== defaultValues.minAmount) {
      updates.push({
        key: 'InvoiceMinAmount',
        value: String(values.minAmount),
      })
    }

    const normalizedAllowed = normalizePaymentMethodValues(
      values.allowedPaymentMethods
    )
    const defaultAllowed = normalizePaymentMethodValues(
      defaultValues.allowedPaymentMethods
    )
    if (JSON.stringify(normalizedAllowed) !== JSON.stringify(defaultAllowed)) {
      updates.push({
        key: 'InvoiceAllowedPaymentMethods',
        value: JSON.stringify(normalizedAllowed),
      })
    }

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }

    form.reset({
      ...values,
      allowedPaymentMethods: normalizedAllowed,
    })
  }

  return (
    <SettingsSection title={t('Invoice Settings')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending || isSubmitting}
            isSaveDisabled={!isDirty}
            saveLabel='Save invoice settings'
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable invoice feature')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Allow users to apply for invoices for their paid orders'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={updateOption.isPending || isSubmitting}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <FormField
            control={form.control}
            name='minAmount'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Minimum invoice amount')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={0}
                    step='0.01'
                    placeholder={t('0')}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'The total paid amount of selected orders must reach this value before invoicing'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='allowedPaymentMethods'
            render={({ field }) => (
              <FormItem>
                <FormLabel>
                  {t('Payment methods allowed for invoicing')}
                </FormLabel>
                <FormControl>
                  <MultiSelect
                    options={paymentMethodOptions}
                    selected={field.value}
                    onChange={(values) => field.onChange(values)}
                    placeholder={t('All payment methods')}
                    disabled={updateOption.isPending || isSubmitting}
                    maxVisibleChips={4}
                    allowCreate
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Leave empty to allow all payment methods. Orders use the payment method recorded when they were paid.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {enabled && (
            <FormField
              control={form.control}
              name='notice'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Invoice notice')}</FormLabel>
                  <FormControl>
                    <Textarea
                      rows={6}
                      placeholder={t(
                        'Invoice notice shown on the invoice page'
                      )}
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'This notice is displayed to users on the invoice application page'
                    )}{' '}
                    {t('Supports Markdown and HTML')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
