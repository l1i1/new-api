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
import { useQuery } from '@tanstack/react-query'
import { Plus, Save, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { getUserModelRateLimits, replaceUserModelRateLimits } from '../api'
import type { UserModelRateLimit } from '../types'

type UserModelRateLimitsSectionProps = {
  userId: number
}

type EditableRateLimit = UserModelRateLimit & { editorKey: string }

let nextEditorKey = 0

const emptyRule = (): EditableRateLimit => ({
  editorKey: `new-${++nextEditorKey}`,
  model_name: '',
  window_seconds: 60,
  max_requests: 60,
  enabled: true,
})

export function UserModelRateLimitsSection(
  props: UserModelRateLimitsSectionProps
) {
  const { t } = useTranslation()
  const [rules, setRules] = useState<EditableRateLimit[]>([])
  const [isSaving, setIsSaving] = useState(false)
  const query = useQuery({
    queryKey: ['user-model-rate-limits', props.userId],
    queryFn: () => getUserModelRateLimits(props.userId),
  })

  useEffect(() => {
    if (query.data?.success && query.data.data) {
      setRules(
        query.data.data.map((rule) => ({
          ...rule,
          editorKey: `persisted-${rule.id}`,
        }))
      )
    }
  }, [query.data])

  const updateRule = (index: number, patch: Partial<EditableRateLimit>) => {
    setRules((current) =>
      current.map((rule, ruleIndex) =>
        ruleIndex === index ? { ...rule, ...patch } : rule
      )
    )
  }

  const saveRules = async () => {
    const normalized: UserModelRateLimit[] = rules.map((rule) => ({
      model_name: rule.model_name.trim(),
      window_seconds: Number(rule.window_seconds),
      max_requests: Number(rule.max_requests),
      enabled: rule.enabled,
    }))
    if (
      normalized.some(
        (rule) =>
          !rule.model_name ||
          rule.window_seconds < 1 ||
          rule.window_seconds > 2_592_000 ||
          rule.max_requests < 1 ||
          rule.max_requests > 1_000_000_000
      )
    ) {
      toast.error(t('Model rate limit values are invalid'))
      return
    }

    setIsSaving(true)
    try {
      const result = await replaceUserModelRateLimits(props.userId, normalized)
      if (!result.success || !result.data) {
        toast.error(result.message || t('Failed to save model rate limits'))
        return
      }
      setRules(
        result.data.map((rule) => ({
          ...rule,
          editorKey: rule.id ? `persisted-${rule.id}` : emptyRule().editorKey,
        }))
      )
      toast.success(t('Model rate limits saved'))
    } catch {
      toast.error(t('Failed to save model rate limits'))
    } finally {
      setIsSaving(false)
    }
  }

  return (
    <div className='space-y-3'>
      <div className='flex items-center justify-between gap-2'>
        <div>
          <h3 className='text-sm font-medium'>{t('Model Rate Limits')}</h3>
          <p className='text-muted-foreground text-xs'>
            {t('Limit requests per user and model by time window.')}
          </p>
        </div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() => setRules((current) => [...current, emptyRule()])}
        >
          <Plus className='mr-1 h-4 w-4' />
          {t('Add rule')}
        </Button>
      </div>

      {query.isLoading && (
        <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
      )}
      {query.isError && (
        <p className='text-destructive text-sm'>
          {t('Failed to load model rate limits')}
        </p>
      )}
      {!query.isLoading && rules.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t('No model-specific rate limits configured.')}
        </p>
      )}

      <div className='space-y-2'>
        {rules.map((rule, index) => (
          <div
            key={rule.editorKey}
            className='grid gap-2 rounded-md border p-3 sm:grid-cols-[minmax(0,1fr)_110px_110px_auto_auto] sm:items-end'
          >
            <div className='space-y-1'>
              <Label htmlFor={`model-rate-limit-model-${index}`}>
                {t('Model')}
              </Label>
              <Input
                id={`model-rate-limit-model-${index}`}
                value={rule.model_name}
                onChange={(event) =>
                  updateRule(index, { model_name: event.target.value })
                }
                placeholder='gpt-5.4-mini'
              />
            </div>
            <div className='space-y-1'>
              <Label htmlFor={`model-rate-limit-window-${index}`}>
                {t('Window (seconds)')}
              </Label>
              <Input
                id={`model-rate-limit-window-${index}`}
                type='number'
                min={1}
                max={2_592_000}
                value={rule.window_seconds}
                onChange={(event) =>
                  updateRule(index, {
                    window_seconds: Number(event.target.value),
                  })
                }
              />
            </div>
            <div className='space-y-1'>
              <Label htmlFor={`model-rate-limit-max-${index}`}>
                {t('Max requests')}
              </Label>
              <Input
                id={`model-rate-limit-max-${index}`}
                type='number'
                min={1}
                max={1_000_000_000}
                value={rule.max_requests}
                onChange={(event) =>
                  updateRule(index, {
                    max_requests: Number(event.target.value),
                  })
                }
              />
            </div>
            <label className='flex items-center gap-2 pb-2 text-sm'>
              <Switch
                checked={rule.enabled}
                onCheckedChange={(checked) =>
                  updateRule(index, { enabled: checked })
                }
              />
              {t('Enabled')}
            </label>
            <Button
              type='button'
              variant='ghost'
              size='icon'
              aria-label={t('Remove rule')}
              title={t('Remove rule')}
              onClick={() =>
                setRules((current) =>
                  current.filter((_rule, ruleIndex) => ruleIndex !== index)
                )
              }
            >
              <Trash2 className='h-4 w-4' />
            </Button>
          </div>
        ))}
      </div>

      <Button type='button' onClick={saveRules} disabled={isSaving}>
        <Save className='mr-1 h-4 w-4' />
        {isSaving ? t('Saving...') : t('Save model limits')}
      </Button>
    </div>
  )
}
