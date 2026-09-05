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
import { Textarea } from '@/components/ui/textarea'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const headHtmlSchema = z.object({
  CustomHeadHTML: z.string(),
})

type HeadHtmlFormValues = z.infer<typeof headHtmlSchema>

type HeadHtmlSectionProps = {
  defaultValue: string
}

export function HeadHtmlSection({ defaultValue }: HeadHtmlSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const form = useForm<HeadHtmlFormValues>({
    resolver: zodResolver(headHtmlSchema),
    defaultValues: {
      CustomHeadHTML: defaultValue ?? '',
    },
  })

  useEffect(() => {
    form.reset({ CustomHeadHTML: defaultValue ?? '' })
  }, [defaultValue, form])

  const onSubmit = async (values: HeadHtmlFormValues) => {
    const normalized = values.CustomHeadHTML ?? ''
    if (normalized === (defaultValue ?? '')) {
      return
    }
    await updateOption.mutateAsync({
      key: 'CustomHeadHTML',
      value: normalized,
    })
  }

  return (
    <SettingsSection title={t('HTML head')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save HTML head'
          />
          <FormField
            control={form.control}
            name='CustomHeadHTML'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Head HTML')}</FormLabel>
                <FormDescription>
                  {t(
                    'Injected verbatim into the <head> of every page on the server, before any script runs — crawlers see it without executing JavaScript. Leave empty to restore the default head.'
                  )}
                </FormDescription>
                <FormControl>
                  <Textarea
                    rows={18}
                    spellCheck={false}
                    className='font-mono text-xs'
                    placeholder='<title>...</title>'
                    {...field}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
