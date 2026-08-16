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
import { useEffect } from 'react'
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

const errorMessageFilterSchema = z.object({
  ErrorMessageFilterEnabled: z.boolean(),
  ErrorMessageFilterPattern: z.string(),
})

type ErrorMessageFilterValues = z.infer<typeof errorMessageFilterSchema>

type ErrorMessageFilterSectionProps = {
  defaultValues: ErrorMessageFilterValues
}

export function ErrorMessageFilterSection({
  defaultValues,
}: ErrorMessageFilterSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const form = useForm<ErrorMessageFilterValues>({
    resolver: zodResolver(errorMessageFilterSchema),
    defaultValues,
  })

  useEffect(() => {
    form.reset(defaultValues)
  }, [defaultValues, form])

  const onSubmit = async (values: ErrorMessageFilterValues) => {
    const updates = Object.entries(values).filter(
      ([key, value]) =>
        value !== defaultValues[key as keyof ErrorMessageFilterValues]
    )

    for (const [key, value] of updates) {
      await updateOption.mutateAsync({ key, value })
    }
  }

  return (
    <SettingsSection title={t('Error message privacy')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save privacy settings'
          />
          <FormField
            control={form.control}
            name='ErrorMessageFilterEnabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>
                    {t('Filter sensitive upstream details')}
                  </FormLabel>
                  <FormDescription>
                    {t(
                      'Removes configured provider names and branding from relay errors returned to API users. Server logs keep the original message.'
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
          <FormField
            control={form.control}
            name='ErrorMessageFilterPattern'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Privacy filter expression')}</FormLabel>
                <FormControl>
                  <Textarea
                    rows={5}
                    spellCheck={false}
                    placeholder={t('Enter a Go RE2 regular expression')}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Matches are removed from relay error messages. Leave blank to keep messages unchanged; expressions use Go RE2 syntax.'
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
