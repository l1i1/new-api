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
import { Share2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Card, CardContent } from '@/components/ui/card'
import { IconBadge } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaAsCny } from '@/lib/currency'

import type { InviteTopUpRewardsData, UserWalletData } from '../types'
import { AffiliateRewardList } from './affiliate-reward-list'

interface AffiliateRewardsCardProps {
  user: UserWalletData | null
  affiliateLink: string
  rewards?: InviteTopUpRewardsData
  loading?: boolean
  rewardsLoading?: boolean
  rewardsError?: boolean
  onRetryRewards?: () => void
}

interface SummaryMetricProps {
  label: string
  value: string
  loading?: boolean
}

function SummaryMetric(props: SummaryMetricProps) {
  return (
    <div className='min-w-0'>
      <div className='text-muted-foreground truncate text-xs'>
        {props.label}
      </div>
      {props.loading ? (
        <Skeleton className='mt-1.5 h-6 w-16' />
      ) : (
        <div
          className='mt-0.5 truncate text-lg font-semibold tabular-nums'
          title={props.value}
        >
          {props.value}
        </div>
      )}
    </div>
  )
}

export function AffiliateRewardsCard(props: AffiliateRewardsCardProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)

  if (props.loading) {
    return (
      <Card data-card-hover='false' className='bg-muted/20 py-0'>
        <CardContent className='p-0'>
          <div className='grid gap-4 border-b p-3 sm:p-4 lg:grid-cols-[minmax(260px,1fr)_minmax(320px,0.9fr)] lg:items-end'>
            <div className='flex items-center gap-2.5'>
              <Skeleton className='size-8' />
              <div className='space-y-2'>
                <Skeleton className='h-5 w-32' />
                <Skeleton className='h-4 w-56 max-w-full' />
              </div>
            </div>
            <Skeleton className='h-9 w-full' />
          </div>
          <div className='grid lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]'>
            <div className='grid grid-cols-2 gap-x-5 gap-y-4 p-3 sm:grid-cols-4 sm:p-4 lg:grid-cols-2 xl:grid-cols-4'>
              {[0, 1, 2, 3].map((index) => (
                <div key={index} className='space-y-2'>
                  <Skeleton className='h-3 w-20' />
                  <Skeleton className='h-6 w-14' />
                </div>
              ))}
            </div>
            <AffiliateRewardList items={[]} loading />
          </div>
        </CardContent>
      </Card>
    )
  }

  let rewardDescription: string
  if (props.rewardsLoading) {
    rewardDescription = t('Loading rewards')
  } else if (props.rewardsError || props.rewards == null) {
    rewardDescription = t('Reward data is temporarily unavailable')
  } else if (!props.rewards.program_enabled) {
    rewardDescription = t(
      'First top-up rewards are currently paused. Your referral link remains available.'
    )
  } else {
    const rewardRate = props.rewards.reward_rate_bps / 100
    const rewardRateLabel = new Intl.NumberFormat(locale, {
      maximumFractionDigits: 2,
    }).format(rewardRate)
    rewardDescription = t(
      'Qualifying users who register through your invite link give you {{rate}}% of their first top-up, credited directly to your main balance.',
      { rate: rewardRateLabel }
    )
  }
  const summary = props.rewards?.summary
  const rewardTotal = formatQuotaAsCny(summary?.total_reward_quota, {
    compact: true,
    digitsLarge: 2,
    digitsSmall: 2,
    locale,
  })
  const rewardDataLoading = props.rewardsLoading && !props.rewardsError

  return (
    <Card data-card-hover='false' className='bg-muted/20 py-0'>
      <CardContent className='p-0'>
        <div className='grid gap-3 border-b p-3 sm:gap-4 sm:p-4 lg:grid-cols-[minmax(260px,1fr)_minmax(320px,0.9fr)] lg:items-end'>
          <div className='flex min-w-0 items-start gap-2.5'>
            <IconBadge tone='chart-3'>
              <Share2 />
            </IconBadge>
            <div className='min-w-0'>
              <h3 className='text-sm font-semibold'>{t('Referral Program')}</h3>
              <p className='text-muted-foreground mt-0.5 text-xs text-pretty'>
                {rewardDescription}
              </p>
            </div>
          </div>

          <div className='min-w-0'>
            <label
              htmlFor='wallet-referral-link'
              className='text-muted-foreground mb-1.5 block text-xs'
            >
              {t('Referral link')}
            </label>
            <div className='flex min-w-0 items-center gap-2'>
              <Input
                id='wallet-referral-link'
                value={props.affiliateLink}
                readOnly
                className='border-muted bg-background/70 h-9 min-w-0 flex-1 font-mono text-xs'
              />
              <CopyButton
                value={props.affiliateLink}
                variant='outline'
                className='bg-background size-9 shrink-0'
                iconClassName='size-4'
                tooltip={t('Copy referral link')}
                aria-label={t('Copy referral link')}
              />
            </div>
          </div>
        </div>

        <div className='grid lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]'>
          <section
            className='grid grid-cols-2 gap-x-5 gap-y-4 p-3 sm:grid-cols-4 sm:p-4 lg:grid-cols-2 xl:grid-cols-4'
            aria-label={t('Referral reward summary')}
          >
            <SummaryMetric
              label={t('Invites')}
              value={String(props.user?.aff_count ?? 0)}
            />
            <SummaryMetric
              label={t('Completed first top-ups')}
              value={
                props.rewardsError ? '-' : String(summary?.applied_count ?? 0)
              }
              loading={rewardDataLoading}
            />
            <SummaryMetric
              label={t('Total earned')}
              value={props.rewardsError ? '-' : rewardTotal}
              loading={rewardDataLoading}
            />
            <SummaryMetric
              label={t('Processing')}
              value={
                props.rewardsError ? '-' : String(summary?.pending_count ?? 0)
              }
              loading={rewardDataLoading}
            />
          </section>
          <AffiliateRewardList
            items={props.rewards?.items ?? []}
            loading={rewardDataLoading}
            error={props.rewardsError}
            onRetry={props.onRetryRewards}
          />
        </div>
      </CardContent>
    </Card>
  )
}
