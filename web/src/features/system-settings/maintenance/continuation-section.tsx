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

import {
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'

const continuationSchema = z.object({
  relay_chat_continuation_on_abnormal: z.boolean(),
  relay_chat_continuation_on_missing: z.boolean(),
  relay_chat_continuation_channels: z.string(),
  relay_chat_continuation_max_steps: z.coerce.number().int().min(0).max(10),
  relay_chat_continuation_abnormal_max_steps: z.coerce
    .number()
    .int()
    .min(0)
    .max(10),
  relay_chat_continuation_abnormal_max_reasoning_bytes: z.coerce
    .number()
    .int()
    .min(1),
  relay_chat_continuation_abnormal_min_reasoning_chars: z.coerce
    .number()
    .int()
    .min(0),
  relay_chat_continuation_missing_max_steps: z.coerce
    .number()
    .int()
    .min(0)
    .max(10),
  relay_chat_continuation_missing_max_reasoning_bytes: z.coerce
    .number()
    .int()
    .min(1),
  relay_chat_continuation_missing_min_reasoning_chars: z.coerce
    .number()
    .int()
    .min(0),
})

type ContinuationFormInput = z.input<typeof continuationSchema>
type ContinuationFormValues = z.output<typeof continuationSchema>

type NumberKey =
  | 'relay_chat_continuation_max_steps'
  | 'relay_chat_continuation_abnormal_max_steps'
  | 'relay_chat_continuation_abnormal_max_reasoning_bytes'
  | 'relay_chat_continuation_abnormal_min_reasoning_chars'
  | 'relay_chat_continuation_missing_max_steps'
  | 'relay_chat_continuation_missing_max_reasoning_bytes'
  | 'relay_chat_continuation_missing_min_reasoning_chars'

export type ContinuationDefaults = ContinuationFormValues

type ContinuationSectionProps = {
  defaultValues: ContinuationDefaults
}

export function ContinuationSection({
  defaultValues,
}: ContinuationSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const form = useForm<ContinuationFormInput, unknown, ContinuationFormValues>({
    resolver: zodResolver(continuationSchema),
    defaultValues,
  })

  useResetForm(form, defaultValues)

  const onSubmit = async (values: ContinuationFormValues) => {
    const changed = (
      Object.keys(values) as Array<keyof ContinuationFormValues>
    ).filter((key) => values[key] !== defaultValues[key])
    for (const key of changed) {
      await updateOption.mutateAsync({ key, value: values[key] })
    }
  }

  const numberField = (name: NumberKey, label: string, description: string) => (
    <FormField
      control={form.control}
      name={name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t(label)}</FormLabel>
          <FormControl>
            <Input
              type='number'
              min={0}
              {...field}
              value={String(field.value)}
            />
          </FormControl>
          <FormDescription>{t(description)}</FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )

  return (
    <SettingsSection title={t('Chat Continuation')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
          />
          <SettingsFormGrid>
            <FormField
              control={form.control}
              name='relay_chat_continuation_on_abnormal'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>
                      {t('900s / abnormal finish continuation')}
                    </FormLabel>
                    <FormDescription>
                      {t(
                        'Resume a reasoning-only round after an abnormal finish (900s cut)'
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
              name='relay_chat_continuation_on_missing'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>
                      {t('60s / missing finish continuation')}
                    </FormLabel>
                    <FormDescription>
                      {t(
                        'Resume a reasoning-only round that ended without any finish_reason'
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
              name='relay_chat_continuation_channels'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Channel whitelist')}</FormLabel>
                  <FormControl>
                    <Input
                      placeholder='37,91'
                      {...field}
                      value={field.value ?? ''}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Comma separated channel ids; empty disables the feature'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            {numberField(
              'relay_chat_continuation_max_steps',
              '32k output-cap continuation steps',
              '0 disables length continuation; each step adds one upstream round'
            )}
            {numberField(
              'relay_chat_continuation_abnormal_max_steps',
              '900s continuation steps',
              'Recommended 1: each round can wait another upstream timeout window'
            )}
            {numberField(
              'relay_chat_continuation_abnormal_max_reasoning_bytes',
              '900s reasoning capture limit (bytes)',
              'Rounds above the limit fall back to the original failure path'
            )}
            {numberField(
              'relay_chat_continuation_abnormal_min_reasoning_chars',
              '900s minimum reasoning (bytes)',
              'Below this length the model had barely started thinking'
            )}
            {numberField(
              'relay_chat_continuation_missing_max_steps',
              'Missing-finish continuation steps',
              'Recommended 1: a repeated 60s wall must not loop'
            )}
            {numberField(
              'relay_chat_continuation_missing_max_reasoning_bytes',
              'Missing-finish reasoning capture limit (bytes)',
              'Rounds above the limit fall back to refund'
            )}
            {numberField(
              'relay_chat_continuation_missing_min_reasoning_chars',
              'Missing-finish minimum reasoning (bytes)',
              'Below this length the cut has too little context to resume'
            )}
          </SettingsFormGrid>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
