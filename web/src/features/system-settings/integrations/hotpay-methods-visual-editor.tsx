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
import { X } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { safeJsonParseWithValidation } from '../utils/json-parser'
import { isArray } from '../utils/json-validators'

const HOTPAY_PREFIX = 'hotpay:'
const COMMON_METHODS = [
  'alipay',
  'wechat_pay',
  'card',
  'apple_pay',
  'google_pay',
]

type HotPayMethodsVisualEditorProps = {
  value: string
  onChange: (value: string) => void
}

/**
 * Normalizes a raw admin input into a canonical "hotpay:<method>" channel id.
 * Missing "hotpay:" prefix, surrounding whitespace, and mixed case are folded
 * in; empty method parts return null so no half-formed id is persisted.
 */
function normalizeChannelId(raw: string): string | null {
  const trimmed = raw.trim().toLowerCase()
  if (!trimmed) return null
  const type = trimmed.startsWith(HOTPAY_PREFIX)
    ? trimmed
    : `${HOTPAY_PREFIX}${trimmed}`
  const method = type.slice(HOTPAY_PREFIX.length).trim()
  if (!method) return null
  return type
}

/**
 * Edits the HotPay channel registry. The registry holds only "hotpay:<method>"
 * channel ids — no display fields. The General PayMethods list is the single
 * source of buyer-visible display, where each registered id is referenced and
 * given its name/icon/min-top-up.
 */
export function HotPayMethodsVisualEditor({
  value,
  onChange,
}: HotPayMethodsVisualEditorProps) {
  const { t } = useTranslation()
  const [inputValue, setInputValue] = useState('')

  const channels = useMemo(() => {
    const parsed = safeJsonParseWithValidation<unknown[]>(value, {
      fallback: [],
      validator: isArray,
      validatorMessage: 'HotPay methods must be a JSON array',
      context: 'HotPay payment methods',
    })

    const seen = new Set<string>()
    const cleaned: string[] = []
    for (const item of parsed) {
      if (typeof item !== 'string') continue
      const normalized = normalizeChannelId(item)
      if (!normalized || seen.has(normalized)) continue
      seen.add(normalized)
      cleaned.push(normalized)
    }
    return cleaned
  }, [value])

  const commit = (next: string[]) => {
    onChange(JSON.stringify(next, null, 2))
  }

  const handleAdd = (raw?: string) => {
    const normalized = normalizeChannelId(raw ?? inputValue)
    if (!normalized || channels.includes(normalized)) return
    const next = [...channels, normalized]
    commit(next)
    setInputValue('')
  }

  const handleRemove = (channel: string) => {
    commit(channels.filter((c) => c !== channel))
  }

  return (
    <div className='space-y-4'>
      <div className='flex flex-col gap-2 sm:flex-row'>
        <Input
          placeholder={t('hotpay:<method>, e.g. hotpay:alipay')}
          value={inputValue}
          onChange={(e) => setInputValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              handleAdd()
            }
          }}
          className='flex-1'
        />
        <Button
          type='button'
          onClick={(e) => {
            e.preventDefault()
            e.stopPropagation()
            handleAdd()
          }}
          className='w-full sm:w-auto'
        >
          {t('Add channel')}
        </Button>
      </div>

      <div className='flex flex-wrap items-center gap-2'>
        <span className='text-muted-foreground text-xs'>
          {t('Quick add:')}
        </span>
        {COMMON_METHODS.map((method) => {
          const channel = `${HOTPAY_PREFIX}${method}`
          const alreadyAdded = channels.includes(channel)
          return (
            <Button
              key={method}
              type='button'
              variant='outline'
              size='sm'
              disabled={alreadyAdded}
              onClick={(e) => {
                e.preventDefault()
                e.stopPropagation()
                handleAdd(channel)
              }}
            >
              {channel}
            </Button>
          )
        })}
      </div>

      <div className='rounded-md border p-3'>
        {channels.length === 0 ? (
          <p className='text-muted-foreground text-sm'>
            {t(
              'No HotPay channels registered. Add a hotpay:<method> id above; only registered ids can be referenced in the General payment methods list.'
            )}
          </p>
        ) : (
          <div className='flex flex-wrap items-center gap-2'>
            {channels.map((channel) => (
              <Badge key={channel} variant='secondary' className='gap-1.5 py-1 pr-1.5 pl-2.5'>
                <code className='font-mono text-xs'>{channel}</code>
                <button
                  type='button'
                  aria-label={t('Remove {{channel}}', { channel })}
                  onClick={(e) => {
                    e.preventDefault()
                    e.stopPropagation()
                    handleRemove(channel)
                  }}
                  className='text-muted-foreground hover:text-foreground rounded-sm p-0.5'
                >
                  <X className='h-3 w-3' />
                </button>
              </Badge>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
