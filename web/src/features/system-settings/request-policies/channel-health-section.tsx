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
import { useEffect, useMemo, useRef } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'
import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

import {
  SettingsControlChildren,
  SettingsControlGroup,
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { safeNumberFieldProps } from '../utils/numeric-field'
import type { HealthSettings, RetrySettings } from './defaults'
import { useSavePolicy } from './use-save-policy'

const numericString = z.string().refine((value) => {
  const trimmed = value.trim()
  if (!trimmed) return true
  return !Number.isNaN(Number(trimmed)) && Number(trimmed) >= 0
}, 'Enter a non-negative number or leave empty')

const channelTestModes = [
  'scheduled_all',
  'auto_ban_only',
  'passive_recovery',
] as const
type ChannelTestMode = (typeof channelTestModes)[number]
const MAX_CHANNEL_TEST_CONCURRENCY = 32

const createChannelHealthSchema = (
  t: (key: string, options?: Record<string, unknown>) => string
) =>
  z
    .object({
      ChannelDisableThreshold: numericString,
      AutomaticDisableChannelEnabled: z.boolean(),
      AutomaticEnableChannelEnabled: z.boolean(),
      AutomaticDisableKeywords: z.string(),
      AutomaticDisableStatusCodes: z.string(),
      RetryTimes: z.coerce.number().int().min(0).max(10),
      AutomaticRetryStatusCodes: z.string(),
      NeverRetryStatusCodes: z.string(),
      MultiKeyCredentialRetryStatusCodes: z.string(),
      AutomaticRetryKeywords: z.string(),
      NeverRetryKeywords: z.string(),
      MultiKeyCredentialRetryKeywords: z.string(),
      monitor_setting: z.object({
        auto_test_channel_enabled: z.boolean(),
        auto_test_channel_minutes: z.coerce
          .number()
          .int()
          .min(1, t('Interval must be at least 1 minute')),
        channel_test_concurrency: z.coerce
          .number()
          .int(t('Enter a positive integer'))
          .min(1, t('Channel test concurrency must be between 1 and 32'))
          .max(
            MAX_CHANNEL_TEST_CONCURRENCY,
            t('Channel test concurrency must be between 1 and 32')
          ),
        channel_test_mode: z.enum(channelTestModes),
      }),
    })
    .superRefine((values, ctx) => {
      const disableParsed = parseHttpStatusCodeRules(
        values.AutomaticDisableStatusCodes
      )
      if (!disableParsed.ok) {
        ctx.addIssue({
          code: 'custom',
          path: ['AutomaticDisableStatusCodes'],
          message: t('Invalid status code rules: {{tokens}}', {
            tokens: disableParsed.invalidTokens.join(', '),
          }),
        })
      }
      for (const [key, value] of [
        ['AutomaticRetryStatusCodes', values.AutomaticRetryStatusCodes],
        ['NeverRetryStatusCodes', values.NeverRetryStatusCodes],
        [
          'MultiKeyCredentialRetryStatusCodes',
          values.MultiKeyCredentialRetryStatusCodes,
        ],
      ] as const) {
        const parsed = parseHttpStatusCodeRules(value)
        if (!parsed.ok) {
          ctx.addIssue({
            code: 'custom',
            path: [key],
            message: t('Invalid status code rules: {{tokens}}', {
              tokens: parsed.invalidTokens.join(', '),
            }),
          })
        }
      }
    })

type ChannelHealthSchema = ReturnType<typeof createChannelHealthSchema>
type ChannelHealthFormValues = z.output<ChannelHealthSchema>
type ChannelHealthFormInput = z.input<ChannelHealthSchema>

type ChannelHealthSectionProps = {
  defaultValues: HealthSettings & RetrySettings
}

function normalizeLineEndings(value: string) {
  return value.replaceAll('\r\n', '\n')
}

type NormalizedChannelHealthValues = {
  ChannelDisableThreshold: string
  AutomaticDisableChannelEnabled: boolean
  AutomaticEnableChannelEnabled: boolean
  AutomaticDisableKeywords: string
  AutomaticDisableStatusCodes: string
  RetryTimes: number
  AutomaticRetryStatusCodes: string
  NeverRetryStatusCodes: string
  MultiKeyCredentialRetryStatusCodes: string
  AutomaticRetryKeywords: string
  NeverRetryKeywords: string
  MultiKeyCredentialRetryKeywords: string
  'monitor_setting.auto_test_channel_enabled': boolean
  'monitor_setting.auto_test_channel_minutes': number
  'monitor_setting.channel_test_concurrency': number
  'monitor_setting.channel_test_mode': ChannelTestMode
}

function normalizeChannelTestMode(value?: string): ChannelTestMode {
  if (value === 'auto_ban_only' || value === 'passive_recovery') {
    return value
  }
  return 'scheduled_all'
}

const buildFormDefaults = (
  defaults: ChannelHealthSectionProps['defaultValues']
): ChannelHealthFormInput => ({
  ChannelDisableThreshold: defaults.ChannelDisableThreshold ?? '',
  AutomaticDisableChannelEnabled: defaults.AutomaticDisableChannelEnabled,
  AutomaticEnableChannelEnabled: defaults.AutomaticEnableChannelEnabled,
  AutomaticDisableKeywords: normalizeLineEndings(
    defaults.AutomaticDisableKeywords ?? ''
  ),
  AutomaticDisableStatusCodes: defaults.AutomaticDisableStatusCodes ?? '',
  RetryTimes: defaults.RetryTimes ?? 0,
  AutomaticRetryStatusCodes: defaults.AutomaticRetryStatusCodes ?? '',
  NeverRetryStatusCodes: defaults.NeverRetryStatusCodes ?? '',
  MultiKeyCredentialRetryStatusCodes:
    defaults.MultiKeyCredentialRetryStatusCodes ?? '',
  AutomaticRetryKeywords: normalizeLineEndings(
    defaults.AutomaticRetryKeywords ?? ''
  ),
  NeverRetryKeywords: normalizeLineEndings(defaults.NeverRetryKeywords ?? ''),
  MultiKeyCredentialRetryKeywords: normalizeLineEndings(
    defaults.MultiKeyCredentialRetryKeywords ?? ''
  ),
  monitor_setting: {
    auto_test_channel_enabled:
      defaults['monitor_setting.auto_test_channel_enabled'],
    auto_test_channel_minutes:
      defaults['monitor_setting.auto_test_channel_minutes'],
    channel_test_concurrency:
      defaults['monitor_setting.channel_test_concurrency'],
    channel_test_mode: normalizeChannelTestMode(
      defaults['monitor_setting.channel_test_mode']
    ),
  },
})

const normalizeDefaults = (
  defaults: ChannelHealthSectionProps['defaultValues']
): NormalizedChannelHealthValues => ({
  ChannelDisableThreshold: (defaults.ChannelDisableThreshold ?? '').trim(),
  AutomaticDisableChannelEnabled: defaults.AutomaticDisableChannelEnabled,
  AutomaticEnableChannelEnabled: defaults.AutomaticEnableChannelEnabled,
  AutomaticDisableKeywords: normalizeLineEndings(
    defaults.AutomaticDisableKeywords ?? ''
  ),
  AutomaticDisableStatusCodes: parseHttpStatusCodeRules(
    defaults.AutomaticDisableStatusCodes ?? ''
  ).normalized,
  RetryTimes: defaults.RetryTimes ?? 0,
  AutomaticRetryStatusCodes: parseHttpStatusCodeRules(
    defaults.AutomaticRetryStatusCodes ?? ''
  ).normalized,
  NeverRetryStatusCodes: parseHttpStatusCodeRules(
    defaults.NeverRetryStatusCodes ?? ''
  ).normalized,
  MultiKeyCredentialRetryStatusCodes: parseHttpStatusCodeRules(
    defaults.MultiKeyCredentialRetryStatusCodes ?? ''
  ).normalized,
  AutomaticRetryKeywords: normalizeLineEndings(
    defaults.AutomaticRetryKeywords ?? ''
  ),
  NeverRetryKeywords: normalizeLineEndings(defaults.NeverRetryKeywords ?? ''),
  MultiKeyCredentialRetryKeywords: normalizeLineEndings(
    defaults.MultiKeyCredentialRetryKeywords ?? ''
  ),
  'monitor_setting.auto_test_channel_enabled':
    defaults['monitor_setting.auto_test_channel_enabled'],
  'monitor_setting.auto_test_channel_minutes':
    defaults['monitor_setting.auto_test_channel_minutes'],
  'monitor_setting.channel_test_concurrency':
    defaults['monitor_setting.channel_test_concurrency'],
  'monitor_setting.channel_test_mode': normalizeChannelTestMode(
    defaults['monitor_setting.channel_test_mode']
  ),
})

const normalizeFormValues = (
  values: ChannelHealthFormValues
): NormalizedChannelHealthValues => ({
  ChannelDisableThreshold: values.ChannelDisableThreshold.trim(),
  AutomaticDisableChannelEnabled: values.AutomaticDisableChannelEnabled,
  AutomaticEnableChannelEnabled: values.AutomaticEnableChannelEnabled,
  AutomaticDisableKeywords: normalizeLineEndings(
    values.AutomaticDisableKeywords
  ),
  AutomaticDisableStatusCodes: parseHttpStatusCodeRules(
    values.AutomaticDisableStatusCodes
  ).normalized,
  RetryTimes: values.RetryTimes,
  AutomaticRetryStatusCodes: parseHttpStatusCodeRules(
    values.AutomaticRetryStatusCodes
  ).normalized,
  NeverRetryStatusCodes: parseHttpStatusCodeRules(values.NeverRetryStatusCodes)
    .normalized,
  MultiKeyCredentialRetryStatusCodes: parseHttpStatusCodeRules(
    values.MultiKeyCredentialRetryStatusCodes
  ).normalized,
  AutomaticRetryKeywords: normalizeLineEndings(values.AutomaticRetryKeywords),
  NeverRetryKeywords: normalizeLineEndings(values.NeverRetryKeywords),
  MultiKeyCredentialRetryKeywords: normalizeLineEndings(
    values.MultiKeyCredentialRetryKeywords
  ),
  'monitor_setting.auto_test_channel_enabled':
    values.monitor_setting.auto_test_channel_enabled,
  'monitor_setting.auto_test_channel_minutes':
    values.monitor_setting.auto_test_channel_minutes,
  'monitor_setting.channel_test_concurrency':
    values.monitor_setting.channel_test_concurrency,
  'monitor_setting.channel_test_mode': values.monitor_setting.channel_test_mode,
})

export function ChannelHealthSection({
  defaultValues,
}: ChannelHealthSectionProps) {
  const { t } = useTranslation()
  const updateOption = useSavePolicy()
  const channelHealthSchema = createChannelHealthSchema(t)
  const baselineRef = useRef<NormalizedChannelHealthValues>(
    normalizeDefaults(defaultValues)
  )

  const formDefaults = useMemo(
    () => buildFormDefaults(defaultValues),
    [defaultValues]
  )

  const form = useForm<
    ChannelHealthFormInput,
    unknown,
    ChannelHealthFormValues
  >({
    resolver: zodResolver(channelHealthSchema),
    defaultValues: formDefaults,
  })

  useResetForm(form, formDefaults)
  useEffect(() => {
    baselineRef.current = normalizeDefaults(defaultValues)
  }, [defaultValues])

  const autoDisableStatusCodes = form.watch('AutomaticDisableStatusCodes')
  const autoRetryStatusCodes = form.watch('AutomaticRetryStatusCodes')
  const neverRetryStatusCodes = form.watch('NeverRetryStatusCodes')
  const multiKeyCredentialRetryStatusCodes = form.watch(
    'MultiKeyCredentialRetryStatusCodes'
  )
  const channelTestMode = form.watch('monitor_setting.channel_test_mode')
  let channelTestModeDescription: string
  switch (channelTestMode) {
    case 'auto_ban_only':
      channelTestModeDescription = t(
        'Periodically checks only channels with auto-disable enabled, excluding manually disabled channels.'
      )
      break
    case 'passive_recovery':
      channelTestModeDescription = t(
        'Does not check healthy channels. It only rechecks auto-disabled channels and restores them after they recover.'
      )
      break
    default:
      channelTestModeDescription = t(
        'Periodically checks all channels except manually disabled ones to detect failures and recover channels automatically.'
      )
  }
  const autoDisableParsed = useMemo(
    () => parseHttpStatusCodeRules(autoDisableStatusCodes),
    [autoDisableStatusCodes]
  )
  const autoRetryParsed = useMemo(
    () => parseHttpStatusCodeRules(autoRetryStatusCodes),
    [autoRetryStatusCodes]
  )
  const neverRetryParsed = useMemo(
    () => parseHttpStatusCodeRules(neverRetryStatusCodes),
    [neverRetryStatusCodes]
  )
  const multiKeyRetryParsed = useMemo(
    () => parseHttpStatusCodeRules(multiKeyCredentialRetryStatusCodes),
    [multiKeyCredentialRetryStatusCodes]
  )

  const onSubmit = async (values: ChannelHealthFormValues) => {
    const normalized = normalizeFormValues(values)
    const updates = (
      Object.keys(normalized) as Array<keyof NormalizedChannelHealthValues>
    ).filter((key) => normalized[key] !== baselineRef.current[key])

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    try {
      await updateOption.mutateAsync(
        Object.fromEntries(updates.map((key) => [key, String(normalized[key])]))
      )
      baselineRef.current = normalized
    } catch (error) {
      handleServerError(error)
    }
  }

  return (
    <SettingsSection title={t('Channel health')}>
      <div className='text-muted-foreground space-y-1 text-sm'>
        <p>{t('Source: global settings. Changes take effect after saving.')}</p>
        <p>
          {form.watch('AutomaticDisableChannelEnabled')
            ? t(
                'Channels must also enable Auto Ban before automatic disabling can take effect.'
              )
            : t(
                'With these settings, automatic disabling is off for all channels.'
              )}
        </p>
        {form.watch('AutomaticEnableChannelEnabled') &&
          !form.watch('monitor_setting.auto_test_channel_enabled') && (
            <p>
              {t(
                'Scheduled recovery is off. Bulk channel tests can still re-enable automatically disabled channels.'
              )}
            </p>
          )}
      </div>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={form.formState.isSubmitting}
          />

          <div className='flex min-w-0 flex-col gap-4'>
            <h4 className='text-sm font-medium'>{t('Request retry')}</h4>
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='RetryTimes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Retry Times')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        max={10}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Number of times to retry failed requests (0-10)')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>

            <div className='flex flex-col gap-1'>
              <p className='text-muted-foreground text-sm'>
                {t(
                  'The rules are checked in this order: never retry, then another key of the same channel, then another channel. A failure that matches nothing is returned to the client.'
                )}
              </p>
            </div>

            <div className='flex flex-col gap-1'>
              <h5 className='text-muted-foreground text-sm font-medium'>
                {t('When never to retry (checked first)')}
              </h5>
            </div>
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='NeverRetryStatusCodes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Never-retry status codes')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g. 504, 524')}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Stop on these status codes, whatever the rules below say.'
                      )}{' '}
                      {neverRetryParsed.ok &&
                        neverRetryParsed.normalized !== field.value.trim() && (
                          <span className='text-muted-foreground'>
                            {t('Normalized:')} {neverRetryParsed.normalized}
                          </span>
                        )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='NeverRetryKeywords'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Never-retry error keywords')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={6}
                        placeholder={t('one keyword per line')}
                        {...field}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Stop when an upstream 4xx error text contains one of these words (case insensitive): another channel would answer the same way, so failing over only spends the retry budget. The default covers the context-window rejections the upstreams return; add a line when an upstream rewords its refusal, and clear the list to turn the rule off.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>

            <div className='flex flex-col gap-1'>
              <h5 className='text-muted-foreground text-sm font-medium'>
                {t('When to retry another key of the same channel')}
              </h5>
            </div>
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='MultiKeyCredentialRetryStatusCodes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Multi-key retry status codes')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g. 402, 403, 429')}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'On a multi-key channel, retry the same channel with another key on these status codes instead of dropping the channel. Use it when each key is a separate upstream account, so a 402 means this key is out of balance.'
                      )}{' '}
                      {multiKeyRetryParsed.ok &&
                        multiKeyRetryParsed.normalized !==
                          field.value.trim() && (
                          <span className='text-muted-foreground'>
                            {t('Normalized:')} {multiKeyRetryParsed.normalized}
                          </span>
                        )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='MultiKeyCredentialRetryKeywords'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Multi-key retry error keywords')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={6}
                        placeholder={t('one keyword per line')}
                        {...field}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Retry the same channel with another key when the upstream error text contains one of these words (case insensitive). The default covers the out-of-balance wordings: the balance belongs to the key, not to the channel. On a single-key channel the request fails over instead.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>

            <div className='flex flex-col gap-1'>
              <h5 className='text-muted-foreground text-sm font-medium'>
                {t('When to retry another channel')}
              </h5>
            </div>
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='AutomaticRetryStatusCodes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Auto-retry status codes')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g. 401, 403, 429, 500-599')}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Retry another channel on these status codes. Accepts comma-separated codes and inclusive ranges; 400 belongs here because an upstream 400 often means this channel cannot serve the request. Local request errors are never retried.'
                      )}{' '}
                      {autoRetryParsed.ok &&
                        autoRetryParsed.normalized !== field.value.trim() && (
                          <span className='text-muted-foreground'>
                            {t('Normalized:')} {autoRetryParsed.normalized}
                          </span>
                        )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='AutomaticRetryKeywords'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Auto-retry error keywords')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={6}
                        placeholder={t('one keyword per line')}
                        {...field}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Retry another channel when the upstream error text contains one of these words (case insensitive), even when the status code alone would not retry. The never-retry rules above still win.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
          </div>

          <Separator />

          <div className='flex min-w-0 flex-col gap-4'>
            <h4 className='text-sm font-medium'>
              {t('Channel health checks')}
            </h4>
            <SettingsFormGrid>
              <SettingsControlGroup>
                <FormField
                  control={form.control}
                  name='monitor_setting.auto_test_channel_enabled'
                  render={({ field }) => (
                    <SettingsSwitchItem>
                      <SettingsSwitchContent>
                        <FormLabel>{t('Scheduled channel tests')}</FormLabel>
                        <FormDescription>
                          {t(
                            'Run background checks using the selected test mode'
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

                <SettingsControlChildren
                  role='group'
                  aria-label={t('Scheduled test options')}
                  className='grid gap-x-5 gap-y-4 lg:grid-cols-2'
                >
                  <FormField
                    control={form.control}
                    name='monitor_setting.channel_test_mode'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Channel test mode')}</FormLabel>
                        <Select
                          items={[
                            {
                              value: 'scheduled_all',
                              label: t('Actively check all channels'),
                            },
                            {
                              value: 'auto_ban_only',
                              label: t(
                                'Actively check auto-disable-enabled channels'
                              ),
                            },
                            {
                              value: 'passive_recovery',
                              label: t('Check channels awaiting recovery only'),
                            },
                          ]}
                          value={field.value}
                          onValueChange={field.onChange}
                        >
                          <FormControl>
                            <SelectTrigger className='w-full'>
                              <SelectValue />
                            </SelectTrigger>
                          </FormControl>
                          <SelectContent alignItemWithTrigger={false}>
                            <SelectGroup>
                              <SelectItem value='scheduled_all'>
                                {t('Actively check all channels')}
                              </SelectItem>
                              <SelectItem value='auto_ban_only'>
                                {t(
                                  'Actively check auto-disable-enabled channels'
                                )}
                              </SelectItem>
                              <SelectItem value='passive_recovery'>
                                {t('Check channels awaiting recovery only')}
                              </SelectItem>
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                        <FormDescription>
                          {channelTestModeDescription}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='monitor_setting.auto_test_channel_minutes'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Test interval (minutes)')}</FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min={1}
                            step={1}
                            {...safeNumberFieldProps(field)}
                          />
                        </FormControl>
                        <FormDescription>
                          {channelTestMode === 'passive_recovery'
                            ? t(
                                'How frequently the system checks auto-disabled channels for recovery'
                              )
                            : t('Time between scheduled channel checks')}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </SettingsControlChildren>
              </SettingsControlGroup>

              <FormField
                control={form.control}
                name='monitor_setting.channel_test_concurrency'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Channel test concurrency')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={MAX_CHANNEL_TEST_CONCURRENCY}
                        step={1}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Maximum number of channels tested at the same time (1-32)'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='AutomaticEnableChannelEnabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Re-enable on success')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Successful scheduled or bulk checks can restore automatically disabled channels. Manually disabled channels stay disabled.'
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
            </SettingsFormGrid>
          </div>

          <Separator />

          <div className='flex min-w-0 flex-col gap-4'>
            <h4 className='text-sm font-medium'>{t('Auto-disable rules')}</h4>
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='AutomaticDisableChannelEnabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Disable on failure')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Apply disable rules to upstream request errors and scheduled or bulk health checks'
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
                name='ChannelDisableThreshold'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>
                      {t('Health check timeout threshold (seconds)')}
                    </FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        step={1}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Scheduled or bulk health checks can disable a channel when this duration is exceeded, if both global and channel auto-disable are enabled.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='AutomaticDisableStatusCodes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Auto-disable status codes')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g. 401, 403, 429, 500-599')}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Accepts comma-separated status codes and inclusive ranges.'
                      )}{' '}
                      {autoDisableParsed.ok &&
                        autoDisableParsed.normalized &&
                        autoDisableParsed.normalized !== field.value.trim() && (
                          <span className='text-muted-foreground'>
                            {t('Normalized:')} {autoDisableParsed.normalized}
                          </span>
                        )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='AutomaticDisableKeywords'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Failure keywords')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={6}
                        placeholder={t('one keyword per line')}
                        {...field}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'If an upstream error contains any of these keywords (case insensitive), the channel will be disabled automatically.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
          </div>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
