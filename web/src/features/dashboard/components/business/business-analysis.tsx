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
import {
  ArrowDownRight,
  CircleAlert,
  Gift,
  Loader2,
  RefreshCw,
  TrendingUp,
  WalletCards,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getBusinessAnalysis } from '@/features/dashboard/api'
import { StatCard } from '@/features/dashboard/components/ui/stat-card'
import type {
  BusinessAnalysisReport,
  BusinessFlowBucket,
  BusinessFlowTotals,
} from '@/features/dashboard/types'

import { BusinessFlowChart } from './business-flow-chart'
import { formatCNY, formatQuotaMoney } from './business-format'
import { BusinessInventory } from './business-inventory'

type BusinessPeriodView = 'daily' | 'weekly'

function sumFlowRows(rows: BusinessFlowBucket[]): BusinessFlowTotals {
  return rows.reduce<BusinessFlowTotals>(
    (totals, row) => ({
      topup_cny: totals.topup_cny + row.topup_cny,
      topup_quota: totals.topup_quota + row.topup_quota,
      consume_quota: totals.consume_quota + row.consume_quota,
      signup_grant_quota: totals.signup_grant_quota + row.signup_grant_quota,
      checkin_quota: totals.checkin_quota + row.checkin_quota,
      manual_add_quota: totals.manual_add_quota + row.manual_add_quota,
      manual_override_increase_quota:
        totals.manual_override_increase_quota +
        row.manual_override_increase_quota,
      non_recharge_increase_quota:
        totals.non_recharge_increase_quota + row.non_recharge_increase_quota,
      net_after_consume_quota:
        totals.net_after_consume_quota + row.net_after_consume_quota,
    }),
    {
      topup_cny: 0,
      topup_quota: 0,
      consume_quota: 0,
      signup_grant_quota: 0,
      checkin_quota: 0,
      manual_add_quota: 0,
      manual_override_increase_quota: 0,
      non_recharge_increase_quota: 0,
      net_after_consume_quota: 0,
    }
  )
}

export function BusinessAnalysis() {
  const { t } = useTranslation()
  const [periodView, setPeriodView] = useState<BusinessPeriodView>('daily')
  const query = useQuery({
    queryKey: ['dashboard', 'business-analysis'],
    queryFn: () =>
      getBusinessAnalysis({ daily_periods: 14, weekly_periods: 8 }),
    staleTime: 60_000,
  })

  if (query.isLoading) return <BusinessAnalysisLoading />

  if (query.isError || !query.data) {
    return (
      <Alert variant='destructive'>
        <CircleAlert />
        <AlertTitle>{t('Failed to load business analysis')}</AlertTitle>
        <AlertDescription className='flex flex-wrap items-center justify-between gap-2'>
          <span>{t('Please try again later.')}</span>
          <Button
            variant='outline'
            size='sm'
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
          >
            {query.isFetching ? (
              <Loader2 className='animate-spin' />
            ) : (
              <RefreshCw />
            )}
            {t('Retry')}
          </Button>
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <BusinessAnalysisContent
      report={query.data}
      query={query}
      periodView={periodView}
      onPeriodViewChange={setPeriodView}
    />
  )
}

function BusinessAnalysisContent(props: {
  report: BusinessAnalysisReport
  query: ReturnType<typeof useQuery<BusinessAnalysisReport>>
  periodView: BusinessPeriodView
  onPeriodViewChange: (view: BusinessPeriodView) => void
}) {
  const { t } = useTranslation()
  const rows =
    props.periodView === 'daily' ? props.report.daily : props.report.weekly
  const totals = useMemo(() => sumFlowRows(rows), [rows])
  const statItems = [
    {
      title: t('Top-ups'),
      value: formatCNY(totals.topup_cny),
      description: t('{{count}} orders · {{users}} paying users', {
        count: rows.reduce((total, row) => total + row.topup_orders, 0),
        users: rows.reduce((total, row) => total + row.topup_users, 0),
      }),
      icon: WalletCards,
      tone: 'accent-1' as const,
    },
    {
      title: t('Consumption'),
      value: formatQuotaMoney(totals.consume_quota, props.report),
      description: t('Quota consumed in the selected periods.'),
      icon: ArrowDownRight,
      tone: 'accent-2' as const,
    },
    {
      title: t('Non-recharge increase'),
      value: formatQuotaMoney(totals.non_recharge_increase_quota, props.report),
      description: t('Grants, check-ins, manual additions, and overrides.'),
      icon: Gift,
      tone: 'accent-3' as const,
    },
    {
      title: t('Net change'),
      value: formatQuotaMoney(totals.net_after_consume_quota, props.report),
      description: t('Top-up quota plus increases minus consumption.'),
      icon: TrendingUp,
      tone: 'accent-1' as const,
    },
  ]

  return (
    <div className='space-y-3 sm:space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <Tabs
          value={props.periodView}
          onValueChange={(value) =>
            props.onPeriodViewChange(value as BusinessPeriodView)
          }
        >
          <TabsList>
            <TabsTrigger value='daily'>{t('Daily')}</TabsTrigger>
            <TabsTrigger value='weekly'>{t('Weekly')}</TabsTrigger>
          </TabsList>
        </Tabs>
        <Button
          variant='outline'
          size='sm'
          onClick={() => void props.query.refetch()}
          disabled={props.query.isFetching}
        >
          {props.query.isFetching ? (
            <Loader2 className='animate-spin' />
          ) : (
            <RefreshCw />
          )}
          {t('Refresh')}
        </Button>
      </div>

      <div className='grid gap-2 sm:grid-cols-2 xl:grid-cols-4'>
        {statItems.map((item) => (
          <div
            key={item.title}
            className='bg-card overflow-hidden rounded-2xl border p-3 shadow-xs sm:p-4'
          >
            <StatCard {...item} />
          </div>
        ))}
      </div>

      <BusinessFlowChart rows={rows} report={props.report} />

      <Alert>
        <CircleAlert />
        <AlertTitle>{t('How to read this report')}</AlertTitle>
        <AlertDescription>
          {t(
            'Operating flow is a period increment report. Inventory is the current account balance and may not reconcile with the selected log retention window.'
          )}
        </AlertDescription>
      </Alert>

      <BusinessInventory report={props.report} />
    </div>
  )
}

function BusinessAnalysisLoading() {
  return (
    <div className='space-y-3 sm:space-y-4'>
      <div className='flex justify-between'>
        <Skeleton className='h-8 w-36' />
        <Skeleton className='h-8 w-24' />
      </div>
      <div className='grid gap-2 sm:grid-cols-2 xl:grid-cols-4'>
        {Array.from({ length: 4 }, (_, index) => (
          <Skeleton key={index} className='h-32 rounded-2xl' />
        ))}
      </div>
      <Skeleton className='h-[360px] rounded-2xl' />
      <div className='grid gap-3 lg:grid-cols-2'>
        <Skeleton className='h-[520px] rounded-2xl' />
        <Skeleton className='h-[520px] rounded-2xl' />
      </div>
    </div>
  )
}
