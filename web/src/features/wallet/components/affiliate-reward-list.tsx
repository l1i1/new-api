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
import { AlertTriangle, Gift, RefreshCw } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaAsCny } from '@/lib/currency'
import { cn } from '@/lib/utils'

import type { InviteTopUpRewardItem, InviteTopUpRewardStatus } from '../types'

interface AffiliateRewardListProps {
  items: InviteTopUpRewardItem[]
  loading?: boolean
  error?: boolean
  onRetry?: () => void
  className?: string
}

function getRewardStatusLabel(
  status: InviteTopUpRewardStatus | string,
  translate: (key: string) => string
) {
  switch (status) {
    case 'applied':
      return { label: translate('Received'), variant: 'success' as const }
    case 'pending':
      return { label: translate('Processing'), variant: 'warning' as const }
    case 'skipped':
      return { label: translate('Not issued'), variant: 'neutral' as const }
    default:
      return { label: translate('Not issued'), variant: 'neutral' as const }
  }
}

function formatRewardDate(
  timestamp: number,
  formatter: Intl.DateTimeFormat
): { dateTime: string; label: string } | null {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return null

  const date = new Date(timestamp * 1000)
  if (!Number.isFinite(date.getTime())) return null

  return {
    dateTime: date.toISOString(),
    label: formatter.format(date),
  }
}

export function AffiliateRewardList(props: AffiliateRewardListProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const dateFormatter = new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })

  let rewardContent: ReactNode
  if (props.loading) {
    rewardContent = (
      <div className='divide-y border-t' aria-label={t('Loading rewards')}>
        {[0, 1, 2].map((index) => (
          <div
            key={index}
            className='flex min-h-14 items-center justify-between gap-3 px-3 py-2.5 sm:px-4'
          >
            <div className='space-y-1.5'>
              <Skeleton className='h-5 w-20' />
              <Skeleton className='h-3 w-28' />
            </div>
            <Skeleton className='h-5 w-20' />
          </div>
        ))}
      </div>
    )
  } else if (props.error) {
    rewardContent = (
      <div className='flex min-h-32 flex-col items-center justify-center gap-2 border-t px-4 py-5 text-center'>
        <AlertTriangle className='text-destructive size-5' aria-hidden='true' />
        <p className='text-sm font-medium'>
          {t('Reward data is temporarily unavailable')}
        </p>
        <p className='text-muted-foreground text-xs text-pretty'>
          {t(
            'Your referral link is still available. Try loading reward data again.'
          )}
        </p>
        {props.onRetry != null && (
          <Button
            variant='outline'
            size='sm'
            onClick={props.onRetry}
            className='mt-1'
          >
            <RefreshCw aria-hidden='true' />
            {t('Retry')}
          </Button>
        )}
      </div>
    )
  } else if (props.items.length === 0) {
    rewardContent = (
      <div className='flex min-h-32 flex-col items-center justify-center gap-2 border-t px-4 py-5 text-center'>
        <Gift className='text-muted-foreground size-5' aria-hidden='true' />
        <p className='text-sm font-medium'>
          {t('No first top-up rewards yet')}
        </p>
        <p className='text-muted-foreground max-w-sm text-xs text-pretty'>
          {t(
            'Rewards will appear here after an invited user completes their first top-up.'
          )}
        </p>
      </div>
    )
  } else {
    rewardContent = (
      <ul
        className='divide-y border-t'
        aria-label={t('Recent first top-up rewards')}
      >
        {props.items.map((reward) => {
          const status = getRewardStatusLabel(reward.status, t)
          const timestamp =
            reward.status === 'applied' && reward.applied_at > 0
              ? reward.applied_at
              : reward.created_at
          const formattedDate = formatRewardDate(timestamp, dateFormatter)
          const amount = formatQuotaAsCny(reward.reward_quota, {
            abbreviate: false,
            digitsLarge: 2,
            digitsSmall: 2,
            locale,
          })

          return (
            <li
              key={reward.id}
              className='flex min-h-14 min-w-0 items-center justify-between gap-3 px-3 py-2.5 sm:px-4'
            >
              <div className='min-w-0'>
                <StatusBadge
                  variant={status.variant}
                  copyable={false}
                  size='sm'
                  label={status.label}
                />
                {formattedDate ? (
                  <time
                    dateTime={formattedDate.dateTime}
                    className='text-muted-foreground mt-1 block truncate text-xs tabular-nums'
                  >
                    {formattedDate.label}
                  </time>
                ) : null}
              </div>
              <span
                className={cn(
                  'max-w-[50%] truncate text-sm font-semibold tabular-nums',
                  reward.status !== 'applied' && 'text-muted-foreground'
                )}
                title={amount}
              >
                {reward.status === 'applied' ? '+' : ''}
                {amount}
              </span>
            </li>
          )
        })}
      </ul>
    )
  }

  return (
    <section
      className={cn(
        'min-w-0 border-t lg:border-t-0 lg:border-l',
        props.className
      )}
      aria-labelledby='recent-invite-rewards-title'
    >
      <div className='flex min-h-11 items-center px-3 sm:px-4'>
        <h4 id='recent-invite-rewards-title' className='text-sm font-medium'>
          {t('Recent first top-up rewards')}
        </h4>
      </div>

      {rewardContent}
    </section>
  )
}
