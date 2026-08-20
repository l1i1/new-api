/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useEffect, useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageActionsPortal } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import {
  getContentModerationConfig,
  getContentModerationLogs,
  updateContentModerationConfig,
} from './content-moderation-api'
import {
  contentModerationSchema,
  toContentModerationFormValues,
  toContentModerationRequest,
  type ContentModerationFormValues,
} from './content-moderation-form'

type ContentModerationSectionProps = {
  defaultValues: Record<string, never>
}

export function ContentModerationSection(
  _props: ContentModerationSectionProps
) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const configQuery = useQuery({
    queryKey: ['content-moderation-config'],
    queryFn: getContentModerationConfig,
  })
  const logsQuery = useQuery({
    queryKey: ['content-moderation-logs'],
    queryFn: getContentModerationLogs,
  })
  const updateMutation = useMutation({
    mutationFn: updateContentModerationConfig,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to update setting'))
        return
      }
      toast.success(t('Setting updated successfully'))
      queryClient.invalidateQueries({ queryKey: ['content-moderation-config'] })
      queryClient.invalidateQueries({ queryKey: ['content-moderation-logs'] })
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to update setting'))
    },
  })

  const defaultValues = useMemo(() => {
    if (configQuery.data?.data) {
      return toContentModerationFormValues(configQuery.data.data)
    }
    return toContentModerationFormValues({
      enabled: false,
      mode: 'observe',
      base_url: 'https://api.openai.com',
      model: 'omni-moderation-latest',
      api_key_count: 0,
      api_key_suffixes: [],
      thresholds: {
        harassment: 0.98,
        'harassment/threatening': 0.9,
        hate: 0.65,
        'hate/threatening': 0.65,
        illicit: 0.95,
        'illicit/violent': 0.95,
        'self-harm': 0.65,
        'self-harm/intent': 0.85,
        'self-harm/instructions': 0.65,
        sexual: 0.65,
        'sexual/minors': 0.65,
        violence: 0.95,
        'violence/graphic': 0.95,
      },
      all_groups: true,
      group_ids: [],
      all_models: true,
      models: [],
      model_filters: [],
      sample_rate: 1,
      timeout_ms: 1500,
      retry_count: 1,
      max_in_flight_per_key: 1,
      queue_wait_ms: 200,
      overload_status: 503,
      key_cooldown_ms: 5000,
      record_non_hits: false,
      block_status: 403,
      block_message: 'Request blocked by content policy',
      email_on_hit: false,
      auto_ban_enabled: false,
      ban_threshold: 10,
      violation_window_hours: 24,
    })
  }, [configQuery.data?.data])

  const form = useForm<ContentModerationFormValues>({
    resolver: zodResolver(contentModerationSchema),
    defaultValues,
  })

  useEffect(() => {
    form.reset(defaultValues)
  }, [defaultValues, form])

  const onSubmit = async (values: ContentModerationFormValues) => {
    await updateMutation.mutateAsync(toContentModerationRequest(values))
  }

  return (
    <div className='space-y-8'>
      <SettingsSection title={t('Content Moderation')}>
        <Form {...form}>
          <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
            <SettingsPageActionsPortal>
              <Button
                type='button'
                size='sm'
                onClick={form.handleSubmit(onSubmit)}
                disabled={updateMutation.isPending}
              >
                {t(updateMutation.isPending ? 'Saving...' : 'Save Changes')}
              </Button>
            </SettingsPageActionsPortal>

            <FormField
              control={form.control}
              name='enabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Enable content moderation')}</FormLabel>
                    <FormDescription>
                      {t('Inspect the latest user turn before upstream relay.')}
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

            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='mode'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Moderation mode')}</FormLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        <SelectItem value='observe'>
                          {t('Observe only')}
                        </SelectItem>
                        <SelectItem value='pre_block'>
                          {t('Block flagged requests')}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t(
                        'Observe records hits; block mode returns the configured status.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='base_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Moderation API base URL')}</FormLabel>
                    <FormControl>
                      <Input placeholder='https://api.openai.com' {...field} />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Must expose an OpenAI-compatible /v1/moderations endpoint.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='model'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Moderation model')}</FormLabel>
                    <FormControl>
                      <Input {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='api_key'
                render={({ field }) => (
                  <FormItem>
                    <div className='flex items-center justify-between gap-2'>
                      <FormLabel>{t('Moderation API keys')}</FormLabel>
                      <div className='flex items-center gap-2'>
                        {form.watch('clear_api_keys') && (
                          <Badge variant='secondary'>{t('Cleared')}</Badge>
                        )}
                        {(configQuery.data?.data.api_key_count ?? 0) > 0 && (
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() => {
                              field.onChange('')
                              form.setValue('clear_api_keys', true, {
                                shouldDirty: true,
                              })
                            }}
                          >
                            {t('Clear')}
                          </Button>
                        )}
                      </div>
                    </div>
                    <FormControl>
                      <Textarea
                        rows={3}
                        placeholder={t('Leave blank to keep existing keys')}
                        {...field}
                        onChange={(event) => {
                          field.onChange(event)
                          if (event.target.value.trim() !== '') {
                            form.setValue('clear_api_keys', false, {
                              shouldDirty: true,
                            })
                          }
                        }}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'One key per line. Existing keys are never returned; current count: {{count}}.',
                        { count: configQuery.data?.data.api_key_count ?? 0 }
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>

            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='max_in_flight_per_key'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Max in-flight per key')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={64}
                        step={1}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Hard provider concurrency limit for each moderation API key.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='queue_wait_ms'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Capacity wait (ms)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={10000}
                        step={10}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Maximum time to wait for a free moderation slot.')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='overload_status'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Overload status')}</FormLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        <SelectItem value='503'>503</SelectItem>
                        <SelectItem value='429'>429</SelectItem>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t('Returned only when pre-block capacity is exhausted.')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='key_cooldown_ms'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Key cooldown (ms)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={100}
                        max={300000}
                        step={100}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Temporarily avoid a key after rate limits or transient failures.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>

            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='sample_rate'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Sample rate')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0.01}
                        max={1}
                        step={0.01}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Use a value from 0.01 to 1.00.')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='timeout_ms'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Moderation timeout (ms)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={120000}
                        step={100}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='retry_count'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Retry count')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={5}
                        step={1}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='block_status'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Block status')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={400}
                        max={599}
                        step={1}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>

            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='all_groups'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Moderate all groups')}</FormLabel>
                      <FormDescription>
                        {t('Disable to restrict checks to listed groups.')}
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
                name='all_models'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Moderate all models')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Disable to restrict checks to listed models or patterns.'
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
                name='group_ids'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Group IDs')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={4}
                        placeholder={t('One group per line')}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='models'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Exact model names')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={4}
                        placeholder={t('One model per line')}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='model_filters'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Model patterns')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={4}
                        placeholder={t('Example: gpt-*')}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Patterns use shell-style wildcards.')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='thresholds'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Category thresholds')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={8}
                        className='font-mono text-xs'
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'JSON map with values from 0 to 1. A category score at or above its threshold is flagged.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>

            <FormField
              control={form.control}
              name='block_message'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Block response message')}</FormLabel>
                  <FormControl>
                    <Input {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='record_non_hits'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Record allowed checks')}</FormLabel>
                      <FormDescription>
                        {t('Store metadata for non-flagged moderation calls.')}
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
                name='email_on_hit'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Email on flagged request')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Notify the account email when a request is flagged.'
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
                name='auto_ban_enabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Automatic account ban')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Disable the account after the violation threshold is reached.'
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
                name='ban_threshold'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Ban threshold')}</FormLabel>
                    <FormControl>
                      <Input type='number' min={1} step={1} {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='violation_window_hours'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Violation window (hours)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={8760}
                        step={1}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
          </SettingsForm>
        </Form>
      </SettingsSection>

      <SettingsSection title={t('Recent moderation logs')}>
        <div className='flex items-center justify-between gap-3'>
          <p className='text-muted-foreground text-sm'>
            {t('Only redacted excerpts and request metadata are retained.')}
          </p>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => logsQuery.refetch()}
            disabled={logsQuery.isFetching}
            title={t('Refresh moderation logs')}
          >
            <RefreshCw className={logsQuery.isFetching ? 'animate-spin' : ''} />
            <span className='sr-only'>{t('Refresh moderation logs')}</span>
          </Button>
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('Time')}</TableHead>
              <TableHead>{t('User')}</TableHead>
              <TableHead>{t('Model')}</TableHead>
              <TableHead>{t('Action')}</TableHead>
              <TableHead>{t('Category')}</TableHead>
              <TableHead>{t('Score')}</TableHead>
              <TableHead>{t('Latency')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(logsQuery.data?.data ?? []).map((entry) => (
              <TableRow key={entry.id}>
                <TableCell>
                  {new Date(entry.created_at * 1000).toLocaleString()}
                </TableCell>
                <TableCell>{entry.user_id || '-'}</TableCell>
                <TableCell className='max-w-48 truncate'>
                  {entry.model || '-'}
                </TableCell>
                <TableCell>
                  <Badge variant={entry.flagged ? 'destructive' : 'secondary'}>
                    {t(entry.action)}
                  </Badge>
                </TableCell>
                <TableCell>{entry.category || '-'}</TableCell>
                <TableCell>
                  {entry.flagged ? entry.score.toFixed(3) : '-'}
                </TableCell>
                <TableCell>
                  {entry.latency_ms ? `${entry.latency_ms} ms` : '-'}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {!logsQuery.isLoading && (logsQuery.data?.data ?? []).length === 0 ? (
          <p className='text-muted-foreground py-8 text-center text-sm'>
            {t('No moderation logs yet.')}
          </p>
        ) : null}
      </SettingsSection>
    </div>
  )
}
