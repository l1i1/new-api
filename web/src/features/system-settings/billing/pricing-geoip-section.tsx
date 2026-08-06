import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
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

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'

const schema = z.object({
  databasePath: z.string().max(1024),
  downloadURL: z.string().refine((value) => {
    const trimmed = value.trim()
    if (!trimmed) return true
    try {
      return new URL(trimmed).protocol === 'https:'
    } catch {
      return false
    }
  }, 'Enter a valid HTTPS URL or leave empty'),
  sha256: z
    .string()
    .refine(
      (value) =>
        !value.trim() || /^(sha256:)?[0-9a-f]{64}$/i.test(value.trim()),
      'Enter a SHA-256 hexadecimal digest or leave empty'
    ),
})

type Values = z.infer<typeof schema>

type Props = {
  defaultValues: Values
}

export function PricingGeoIPSection({ defaultValues }: Props) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues,
  })
  useResetForm(form, defaultValues)

  const onSubmit = async (values: Values) => {
    const fields: Array<[keyof Values, string]> = [
      ['databasePath', values.databasePath.trim()],
      ['downloadURL', values.downloadURL.trim()],
      ['sha256', values.sha256.trim()],
    ]
    const keys: Record<keyof Values, string> = {
      databasePath: 'pricing_geoip.db',
      downloadURL: 'pricing_geoip.url',
      sha256: 'pricing_geoip.sha256',
    }
    const changed = fields.filter(
      ([field, value]) => value !== defaultValues[field]
    )
    if (changed.length === 0) {
      toast.info(t('No changes to save'))
      return
    }
    for (const [field, value] of changed) {
      await updateOption.mutateAsync({ key: keys[field], value })
    }
    form.reset(values)
  }

  return (
    <SettingsSection title={t('Pricing GeoIP Compliance')}>
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
                    'Path for the MaxMind country database. Leave empty to use PRICING_GEOIP_DB or the default path.'
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
