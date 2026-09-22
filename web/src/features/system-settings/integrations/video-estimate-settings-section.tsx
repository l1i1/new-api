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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

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
import { useStatus } from '@/hooks/use-status'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import { isCompleteHttpUrl, removeTrailingSlash } from './utils'

const BASE_URL_KEY = 'video_estimate_setting.base_url'
const API_KEY_KEY = 'video_estimate_setting.api_key'

const createVideoEstimateSchema = (t: (key: string) => string) =>
  z.object({
    // A scheme with no host passes a prefix check but builds a request that can
    // never work, so the check parses the value instead.
    [BASE_URL_KEY]: z
      .string()
      .refine(
        isCompleteHttpUrl,
        t('Provide a valid URL starting with http:// or https://')
      ),
    [API_KEY_KEY]: z.string(),
    clearApiKey: z.boolean(),
  })

type VideoEstimateFormValues = z.infer<
  ReturnType<typeof createVideoEstimateSchema>
>

type VideoEstimateSettingsSectionProps = {
  defaultValues: {
    [BASE_URL_KEY]: string
    [API_KEY_KEY]: string
    clearApiKey: boolean
  }
}

export function VideoEstimateSettingsSection({
  defaultValues,
}: VideoEstimateSettingsSectionProps) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const updateOption = useUpdateOption()
  const schema = createVideoEstimateSchema(t)

  // The stored key is never sent to the browser (the options API withholds
  // values whose key ends in "Key"), so the field starts blank and a blank
  // field means "keep what is stored".
  const form = useForm<VideoEstimateFormValues>({
    resolver: zodResolver(schema),
    defaultValues,
  })

  useResetForm(form, defaultValues)

  const onSubmit = async (values: VideoEstimateFormValues) => {
    const sanitizedBaseUrl = removeTrailingSlash(values[BASE_URL_KEY])
    const sanitizedKey = values[API_KEY_KEY].trim()
    const initialBaseUrl = removeTrailingSlash(defaultValues[BASE_URL_KEY])
    const clearing = values.clearApiKey && !sanitizedKey

    const updates: Array<{ key: string; value: string }> = []

    if (sanitizedBaseUrl !== initialBaseUrl) {
      updates.push({ key: BASE_URL_KEY, value: sanitizedBaseUrl })
    }

    if (clearing) {
      // An explicit clear is the switch that takes the endpoint dark when no
      // environment variable provides one.
      updates.push({ key: API_KEY_KEY, value: '' })
    } else if (sanitizedKey !== '') {
      updates.push({ key: API_KEY_KEY, value: sanitizedKey })
    }

    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }
  }

  const configured = Boolean(status?.video_estimate_configured)

  return (
    <SettingsSection title={t('Video token estimation')}>
      <p className='text-muted-foreground text-sm'>
        {configured
          ? t(
              'Configured. Video requests on channels marked for estimation are priced with the provider tokenizer.'
            )
          : t(
              'Not configured. Video requests on channels marked for estimation are priced from the local container model, which is offline and accurate to within about 5%.'
            )}
      </p>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save video estimation settings'
          />
          <FormField
            control={form.control}
            name={BASE_URL_KEY}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Tokenizer base URL')}</FormLabel>
                <FormControl>
                  <Input
                    type='url'
                    inputMode='url'
                    placeholder={t('https://api.moonshot.cn/v1')}
                    autoComplete='off'
                    {...field}
                    onChange={(event) => field.onChange(event.target.value)}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'The estimate endpoint root. Leave blank to use the environment variable, or the documented Moonshot endpoint when that is unset.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name={API_KEY_KEY}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Tokenizer API key')}</FormLabel>
                <FormControl>
                  <Input
                    type='password'
                    placeholder={t('Enter new key to update')}
                    autoComplete='new-password'
                    {...field}
                    onChange={(event) => field.onChange(event.target.value)}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Sending a user video to this endpoint is a cross-supplier data flow; configure a key only when that is intended. Leave blank to keep the existing key.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='clearApiKey'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Remove the stored key')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Takes effect on save, unless an environment variable still provides a key.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
