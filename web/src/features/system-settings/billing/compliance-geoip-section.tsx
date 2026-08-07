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
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

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
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import { safeNumberFieldProps } from '../utils/numeric-field'

const splitList = (value: string) =>
  value
    .split(/[,，;\n\r]+/)
    .map((item) => item.trim())
    .filter(Boolean)

const listSchema = (
  itemValidator: (item: string) => boolean,
  message: string
) =>
  z.string().refine((value) => {
    const items = splitList(value)
    return items.length >= 1 && items.length <= 64 && items.every(itemValidator)
  }, message)

const normalizeList = (value: string, normalize: (item: string) => string) =>
  [...new Set(splitList(value).map(normalize))].join(',')

const createSchema = (t: TFunction) =>
  z.object({
    enabled: z.boolean(),
    countryCodes: listSchema(
      (item) => /^[a-z]{2}$/i.test(item),
      t('Enter 1-64 two-letter country codes')
    ),
    modelKeywords: listSchema(
      (item) => [...item].length <= 64,
      t('Enter 1-64 model keywords, up to 64 characters each')
    ),
    groupKeywords: listSchema(
      (item) => [...item].length <= 64,
      t('Enter 1-64 group keywords, up to 64 characters each')
    ),
    retryBackoffMinutes: z.coerce
      .number()
      .int(t('Enter an integer from 1 to 1440'))
      .min(1, t('Enter an integer from 1 to 1440'))
      .max(1440, t('Enter an integer from 1 to 1440')),
    databasePath: z
      .string()
      .max(1024, t('The database path cannot exceed 1024 characters')),
    downloadURL: z.string().refine((value) => {
      const trimmed = value.trim()
      if (!trimmed) return true
      try {
        return new URL(trimmed).protocol === 'https:'
      } catch {
        return false
      }
    }, t('Enter a valid HTTPS URL or leave empty')),
    sha256: z
      .string()
      .refine(
        (value) =>
          !value.trim() || /^(sha256:)?[0-9a-f]{64}$/i.test(value.trim()),
        t('Enter a SHA-256 hexadecimal digest or leave empty')
      ),
  })

type Values = z.infer<ReturnType<typeof createSchema>>

type Props = {
  defaultValues: Values
}

export function ComplianceGeoIPSection({ defaultValues }: Props) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const schema = createSchema(t)
  const form = useForm<Values>({
    resolver: zodResolver(schema) as unknown as Resolver<Values>,
    defaultValues,
  })
  useResetForm(form, defaultValues)

  const onSubmit = async (values: Values) => {
    const normalized: Values = {
      ...values,
      countryCodes: normalizeList(values.countryCodes, (item) =>
        item.toUpperCase()
      ),
      modelKeywords: normalizeList(values.modelKeywords, (item) =>
        item.toLowerCase()
      ),
      groupKeywords: normalizeList(values.groupKeywords, (item) =>
        item.toLowerCase()
      ),
      databasePath: values.databasePath.trim(),
      downloadURL: values.downloadURL.trim(),
      sha256: values.sha256.trim(),
    }
    const updates: Array<{ key: string; value: string }> = []
    const keys: Record<keyof Values, string> = {
      enabled: 'compliance_geoip.enabled',
      countryCodes: 'compliance_geoip.country_codes',
      modelKeywords: 'compliance_geoip.model_keywords',
      groupKeywords: 'compliance_geoip.group_keywords',
      retryBackoffMinutes: 'compliance_geoip.retry_backoff_minutes',
      databasePath: 'compliance_geoip.db',
      downloadURL: 'compliance_geoip.url',
      sha256: 'compliance_geoip.sha256',
    }
    for (const field of Object.keys(keys) as Array<keyof Values>) {
      if (normalized[field] !== defaultValues[field]) {
        updates.push({ key: keys[field], value: String(normalized[field]) })
      }
    }
    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }
    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }
    form.reset(normalized)
  }

  return (
    <SettingsSection title={t('GeoIP Compliance')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            onReset={() => form.reset(defaultValues)}
            isSaving={updateOption.isPending || form.formState.isSubmitting}
            isResetDisabled={!form.formState.isDirty}
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable discovery compliance')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Filter configured models and groups for requests from configured countries.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={
                      updateOption.isPending || form.formState.isSubmitting
                    }
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
          <FormField
            control={form.control}
            name='countryCodes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Restricted country codes')}</FormLabel>
                <FormControl>
                  <Textarea {...field} rows={2} placeholder='CN' />
                </FormControl>
                <FormDescription>
                  {t(
                    'Two-letter country codes separated by commas, semicolons, or new lines.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='modelKeywords'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Restricted model keywords')}</FormLabel>
                <FormControl>
                  <Textarea
                    {...field}
                    rows={3}
                    placeholder='gpt, gemini, claude, grok'
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Model identifiers containing any keyword are hidden from discovery responses.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='groupKeywords'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Restricted group keywords')}</FormLabel>
                <FormControl>
                  <Textarea
                    {...field}
                    rows={3}
                    placeholder='gpt, gemini, claude, grok, genpic'
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Groups containing any keyword are hidden from discovery responses.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='retryBackoffMinutes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Download retry interval')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    max={1440}
                    step={1}
                    {...safeNumberFieldProps(field)}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Minutes to wait before retrying a failed GeoIP database download.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='databasePath'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('GeoIP database path')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    placeholder={t('Use environment or default path')}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Path for the MaxMind country database. Leave empty to use COMPLIANCE_GEOIP_DB or the default path.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='downloadURL'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('GeoIP download URL')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type='url'
                    placeholder={t('Use the default GeoLite2 source')}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Used only when the database is missing. The URL must use HTTPS.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='sha256'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('GeoIP SHA-256 checksum')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    placeholder={t('Optional 64-character hexadecimal digest')}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Optional integrity check for an automatically downloaded database.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
