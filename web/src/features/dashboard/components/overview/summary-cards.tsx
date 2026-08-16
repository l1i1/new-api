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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowRight } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import { getUserQuotaDates } from '@/features/dashboard/api'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { useStatus } from '@/hooks/use-status'
import { formatNumber, formatQuota } from '@/lib/format'
import { computeTimeRange } from '@/lib/time'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

const SUMMARY_SPARKLINE_BUCKETS = 12

function getBucketIndex(
  timestamp: number,
  start: number,
  end: number,
  bucketCount: number
): number {
  if (end <= start) return 0
  const ratio = (timestamp - start) / (end - start)
  return Math.min(bucketCount - 1, Math.max(0, Math.floor(ratio * bucketCount)))
}

function buildUsageSparkline(
  data: QuotaDataItem[],
  start: number,
  end: number
): number[] {
  const usage = Array.from({ length: SUMMARY_SPARKLINE_BUCKETS }, () => 0)

  for (const item of data) {
    const timestamp = Number(item.created_at) || start
    const index = getBucketIndex(
      timestamp,
      start,
      end,
      SUMMARY_SPARKLINE_BUCKETS
    )
    usage[index] += Number(item.quota) || 0
  }

  return usage
}

function getRunwayDays(
  remainQuota: number,
  recentUsage: number
): number | null {
  if (remainQuota <= 0 || recentUsage <= 0) return null
  const days = remainQuota / recentUsage
  if (!Number.isFinite(days)) return null
  return days
}

type HealthLevel = 'healthy' | 'caution' | 'critical'

function getHealthLevel(remainQuota: number, recentUsage: number): HealthLevel {
  if (remainQuota <= 0) return 'critical'
  const days = getRunwayDays(remainQuota, recentUsage)
  if (days !== null && days < 3) return 'caution'
  return 'healthy'
}

const HEALTH_CONFIG: Record<
  HealthLevel,
  { dotClass: string; labelKey: string }
> = {
  healthy: {
    dotClass: 'bg-success',
    labelKey: 'Healthy',
  },
  caution: {
    dotClass: 'bg-warning',
    labelKey: 'Low balance',
  },
  critical: {
    dotClass: 'bg-destructive',
    labelKey: 'Balance depleted',
  },
}

function MiniSparkline(props: { values: number[] }) {
  const points = useMemo(() => {
    const values = props.values.map((value) => Math.max(0, Number(value) || 0))
    const max = values.length ? Math.max(...values) : 0
    if (max <= 0) return ''

    const width = 100
    const height = 24
    const padding = 2

    return values
      .map((value, index) => {
        const x =
          values.length === 1
            ? width / 2
            : (index / (values.length - 1)) * width
        const ratio = value / max
        const y = height - padding - ratio * (height - padding * 2)
        return `${x},${y}`
      })
      .join(' ')
  }, [props.values])

  if (!points) return null

  return (
    <svg
      viewBox='0 0 100 24'
      preserveAspectRatio='none'
      className='text-muted-foreground/60 mt-1 h-6 w-full'
      aria-hidden='true'
    >
      <polyline
        points={points}
        fill='none'
        stroke='currentColor'
        strokeWidth='1.5'
        strokeLinecap='round'
        strokeLinejoin='round'
        vectorEffect='non-scaling-stroke'
      />
    </svg>
  )
}

function MetricCell(props: {
  label: string
  value: string
  loading?: boolean
  sparkline?: number[]
}) {
  return (
    <div className='border-border -mt-px -ml-px flex flex-col gap-1 border px-4 py-3'>
      <span className='text-muted-foreground truncate text-xs'>
        {props.label}
      </span>
      {props.loading ? (
        <Skeleton className='h-7 w-20' />
      ) : (
        <span className='truncate font-mono text-lg font-semibold tabular-nums sm:text-xl'>
          {props.value}
        </span>
      )}
      {props.sparkline ? <MiniSparkline values={props.sparkline} /> : null}
    </div>
  )
}

export function SummaryCards() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const { loading } = useStatus()

  const summaryTimeRange = useMemo(() => computeTimeRange(1), [])
  const remainQuota = Number(user?.quota ?? 0)
  const usedQuota = Number(user?.used_quota ?? 0)
  const requestCount = Number(user?.request_count ?? 0)

  const usageTrendQuery = useQuery({
    queryKey: [
      'dashboard',
      'overview',
      'summary-sparklines',
      summaryTimeRange.start_timestamp,
      summaryTimeRange.end_timestamp,
    ],
    queryFn: async () =>
      getUserQuotaDates({
        start_timestamp: summaryTimeRange.start_timestamp,
        end_timestamp: summaryTimeRange.end_timestamp,
        default_time: 'hour',
      }),
    staleTime: 60 * 1000,
  })

  const usageSparkline = useMemo(
    () =>
      buildUsageSparkline(
        usageTrendQuery.data?.data ?? [],
        summaryTimeRange.start_timestamp,
        summaryTimeRange.end_timestamp
      ),
    [
      summaryTimeRange.end_timestamp,
      summaryTimeRange.start_timestamp,
      usageTrendQuery.data?.data,
    ]
  )

  const recentUsage = useMemo(
    () =>
      (usageTrendQuery.data?.data ?? []).reduce(
        (total, item) => total + (Number(item.quota) || 0),
        0
      ),
    [usageTrendQuery.data?.data]
  )

  const healthLevel = getHealthLevel(remainQuota, recentUsage)
  const healthCfg = HEALTH_CONFIG[healthLevel]
  const runwayDays = getRunwayDays(remainQuota, recentUsage)

  let runwayDisplay: string
  if (runwayDays !== null) {
    if (runwayDays < 1) {
      runwayDisplay = t('Less than 1 day left')
    } else if (runwayDays > 999) {
      runwayDisplay = `999+ ${t('days')}`
    } else {
      runwayDisplay = `~${formatNumber(Math.floor(runwayDays))} ${t('days')}`
    }
  } else if (remainQuota <= 0) {
    runwayDisplay = t('Balance depleted')
  } else {
    runwayDisplay = t('No recent usage')
  }

  const metrics = [
    {
      key: 'todayUsage',
      label: t('Last 24h usage'),
      value: formatQuota(recentUsage),
      sparkline: usageSparkline,
    },
    {
      key: 'usage',
      label: t('Historical Usage'),
      value: formatQuota(usedQuota),
    },
    {
      key: 'requests',
      label: t('Request Count'),
      value: formatNumber(requestCount),
    },
  ]

  return (
    <section className='bg-card border-border border'>
      <div className='border-border flex items-center justify-between gap-3 border-b px-4 py-2.5 sm:px-5'>
        <h2 className='text-sm font-semibold'>{t('Usage at a glance')}</h2>
        <Link
          to='/wallet'
          className='text-muted-foreground hover:text-foreground focus-visible:ring-ring inline-flex shrink-0 items-center gap-1 rounded-none text-xs font-medium transition-colors outline-none focus-visible:ring-2'
        >
          {t('Wallet')}
          <ArrowRight className='size-3' aria-hidden='true' />
        </Link>
      </div>

      <div className='grid grid-cols-2 sm:grid-cols-4'>
        <div className='border-border col-span-2 -mt-px -ml-px flex flex-col gap-1 border px-4 py-3 sm:col-span-1'>
          <span className='flex items-center gap-1.5'>
            <span
              className={cn('size-1.5 shrink-0 rounded-full', healthCfg.dotClass)}
              aria-hidden='true'
            />
            <span className='text-muted-foreground truncate text-xs'>
              {t('Credit remaining')}
            </span>
          </span>
          {loading ? (
            <Skeleton className='h-7 w-24' />
          ) : (
            <span className='truncate font-mono text-lg font-semibold tabular-nums sm:text-xl'>
              {formatQuota(remainQuota)}
            </span>
          )}
          <span
            className={cn(
              'text-muted-foreground truncate text-[11px]',
              healthLevel === 'critical' && 'text-destructive',
              healthLevel === 'caution' && 'text-warning'
            )}
            title={`${t('Runway')}: ${runwayDisplay} · ${t(healthCfg.labelKey)}`}
          >
            {t('Runway')}: {runwayDisplay}
          </span>
        </div>

        {metrics.map((metric) => (
          <MetricCell
            key={metric.key}
            label={metric.label}
            value={metric.value}
            loading={loading}
            sparkline={metric.sparkline}
          />
        ))}
      </div>
    </section>
  )
}
