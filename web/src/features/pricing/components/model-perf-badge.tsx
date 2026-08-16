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
import { memo } from 'react'
import { useTranslation } from 'react-i18next'

import { getSuccessRateDotClass } from '@/features/performance-metrics/lib/format'
import { cn } from '@/lib/utils'

export type ModelPerfBadgeData = {
  avg_ttft_ms?: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  recent_success_rates?: number[]
}

export interface ModelPerfBadgeProps extends React.HTMLAttributes<HTMLDivElement> {
  perf: ModelPerfBadgeData | undefined
}

function formatCompactNumber(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '—'
  return value > 1 ? String(Math.round(value)) : value.toFixed(1)
}

function formatCompactLatency(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  if (ms >= 1_000) return `${formatCompactNumber(ms / 1_000)}s`
  return `${formatCompactNumber(ms)}ms`
}

function formatCompactThroughput(tps: number): string {
  if (!Number.isFinite(tps) || tps <= 0) return '—'
  if (tps >= 1_000) return `${formatCompactNumber(tps / 1_000)}Kt`
  return `${formatCompactNumber(tps)}t`
}

const BAR_SLOTS = ['first', 'second', 'third'] as const
const BAR_HEIGHT_CLASSES = ['h-2', 'h-2.5', 'h-3'] as const
const BAR_EMPTY_CLASSES = [
  'bg-muted-foreground/10',
  'bg-muted-foreground/15',
  'bg-muted-foreground/15',
] as const

export const ModelPerfBadge = memo(function ModelPerfBadge(
  props: ModelPerfBadgeProps
) {
  const { t } = useTranslation()

  const ttft =
    props.perf?.avg_ttft_ms && props.perf.avg_ttft_ms > 0
      ? formatCompactLatency(props.perf.avg_ttft_ms)
      : '—'
  const throughput = props.perf
    ? formatCompactThroughput(props.perf.avg_tps)
    : '—'

  const recentRates =
    props.perf?.recent_success_rates?.filter((rate) => Number.isFinite(rate)) ??
    []
  let statusRates: number[] = []
  if (recentRates.length > 0) {
    statusRates = recentRates.slice(-3)
  } else if (props.perf) {
    statusRates = [props.perf.success_rate]
  }
  const statusBars = [
    ...Array(Math.max(0, 3 - statusRates.length)).fill(null),
    ...statusRates,
  ].slice(-3)

  return (
    <div
      className={cn(
        'hidden w-[132px] grid-cols-[38px_48px_30px] gap-x-2 text-right tabular-nums min-[460px]:grid',
        props.className
      )}
    >
      <div title={t('Average TTFT')} className='min-w-0'>
        <div className='text-muted-foreground/55 text-[10px] leading-4'>
          {t('TTFT short')}
        </div>
        <div className='text-muted-foreground/80 font-mono text-xs leading-4 whitespace-nowrap'>
          {ttft}
        </div>
      </div>
      <div title={t('Throughput')} className='min-w-0'>
        <div className='text-muted-foreground/55 truncate text-[10px] leading-4'>
          {t('Throughput short')}
        </div>
        <div className='text-muted-foreground/80 font-mono text-xs leading-4 whitespace-nowrap'>
          {throughput}
        </div>
      </div>
      <div
        title={
          props.perf
            ? `${t('Success rate')}: ${props.perf.success_rate.toFixed(1)}%`
            : t('Success rate')
        }
        className='min-w-0'
      >
        <div className='text-muted-foreground/55 truncate text-[10px] leading-4'>
          {t('Status short')}
        </div>
        <div className='flex h-4 items-center justify-end gap-0.5'>
          {statusBars.map((rate, index) => (
            <span
              key={BAR_SLOTS[index]}
              className={cn(
                'w-1 rounded-full',
                BAR_HEIGHT_CLASSES[index],
                rate == null
                  ? BAR_EMPTY_CLASSES[index]
                  : getSuccessRateDotClass(rate)
              )}
            />
          ))}
        </div>
      </div>
    </div>
  )
})
