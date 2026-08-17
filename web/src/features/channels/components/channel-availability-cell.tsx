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
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'

import { isTagAggregateRow, type TagRow } from '../lib'
import {
  aggregateChannelAvailability,
  type ChannelAvailabilityPoint,
  type ChannelAvailabilitySeries,
} from '../lib/channel-observability'
import type { Channel } from '../types'
import { useChannels } from './channels-provider'

type ChannelAvailabilityCellProps = {
  channel: Channel
  series: ChannelAvailabilitySeries
}

function pointClassName(point: ChannelAvailabilityPoint): string {
  if (point.requestCount === 0) return 'bg-muted-foreground/30'
  if (point.successRate >= 99) return 'bg-success'
  if (point.successRate >= 90) return 'bg-warning'
  return 'bg-destructive'
}

function formatTime(timestamp: number, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(timestamp * 1000))
}

function aggregateTagSeries(
  tagRow: TagRow,
  series: ChannelAvailabilitySeries
): ChannelAvailabilityPoint[] {
  const children = tagRow.children ?? []
  const childSeries = children
    .map((child) => series[child.id])
    .filter((value): value is ChannelAvailabilityPoint[] => Boolean(value))
  if (childSeries.length === 0) return []

  const pointCount = Math.max(...childSeries.map((items) => items.length))
  return Array.from({ length: pointCount }, (_, index) => {
    const points = childSeries
      .map((items) => items[index])
      .filter((value): value is ChannelAvailabilityPoint => Boolean(value))
    return aggregateChannelAvailability(
      points.map((point) => ({
        channel_id: tagRow.id,
        credential_id: 0,
        requested_model: '',
        group: '',
        protocol: '',
        request_count: point.requestCount,
        request_success_count: point.successCount,
        request_failure_count: point.failureCount,
        attempt_count: point.requestCount,
        request_success_rate: point.successRate,
        attempt_success_rate: point.successRate,
        cache_hit_rate: 0,
        cache_token_rate: 0,
        avg_latency_ms: point.ttftMs,
        p95_latency_ms: point.ttftMs,
        avg_request_latency_ms: point.ttftMs,
        p95_request_latency_ms: point.ttftMs,
        avg_ttft_ms: point.ttftMs,
        ttft_count: point.ttftSampleCount,
        p95_ttft_ms: 0,
        avg_upstream_frt_ms: 0,
        p95_upstream_frt_ms: 0,
        sample_coverage: 100,
        usage_coverage: 0,
        sample_sufficient: true,
        usage_sufficient: false,
      })),
      points[0]?.bucketStart ?? 0,
      points[0]?.bucketEnd ?? 0
    )
  })
}

export function ChannelAvailabilityCell(props: ChannelAvailabilityCellProps) {
  const { t, i18n } = useTranslation()
  const { setCurrentRow, setOpen } = useChannels()
  const isTagRow = isTagAggregateRow(props.channel)
  const points = isTagRow
    ? aggregateTagSeries(props.channel as TagRow, props.series)
    : (props.series[props.channel.id] ?? [])
  const requestCount = points.reduce(
    (sum, point) => sum + point.requestCount,
    0
  )
  const successCount = points.reduce(
    (sum, point) => sum + point.successCount,
    0
  )
  const failureCount = points.reduce(
    (sum, point) => sum + point.failureCount,
    0
  )
  const successRate = requestCount > 0 ? (successCount / requestCount) * 100 : 0
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language) || 'en-US'

  const openObservability = () => {
    if (isTagRow) return
    setCurrentRow(props.channel)
    setOpen('observability')
  }

  if (points.length === 0) {
    return (
      <div
        className='text-muted-foreground hover:bg-muted/50 flex min-w-[180px] cursor-pointer items-center rounded-sm py-1 text-xs'
        role={isTagRow ? undefined : 'button'}
        tabIndex={isTagRow ? undefined : 0}
        onClick={openObservability}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault()
            openObservability()
          }
        }}
        aria-label={isTagRow ? undefined : t('Channel observability')}
      >
        -
      </div>
    )
  }

  return (
    <div
      className='text-foreground hover:bg-muted/50 focus-visible:ring-ring flex min-w-[180px] cursor-pointer flex-col gap-1.5 rounded-sm py-0.5 outline-none focus-visible:ring-2'
      role={isTagRow ? undefined : 'button'}
      tabIndex={isTagRow ? undefined : 0}
      onClick={openObservability}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          openObservability()
        }
      }}
      aria-label={isTagRow ? undefined : t('Channel observability')}
    >
      <div className='flex items-center gap-2 text-xs'>
        <span className='text-success whitespace-nowrap'>
          ✓ {successCount.toLocaleString()}
        </span>
        <span className='text-destructive whitespace-nowrap'>
          × {failureCount.toLocaleString()}
        </span>
        <span className='text-muted-foreground ml-auto font-mono'>
          {successRate.toFixed(1)}%
        </span>
      </div>
      <TooltipProvider delay={150}>
        <div className='flex h-3 items-center gap-0.5'>
          {points.map((point) => (
            <Tooltip key={`${point.bucketStart}-${point.bucketEnd}`}>
              <TooltipTrigger
                render={
                  <span
                    className={`h-2.5 min-w-1 flex-1 rounded-[2px] transition-opacity hover:opacity-70 ${pointClassName(point)}`}
                    tabIndex={0}
                    aria-label={`${formatTime(point.bucketStart, locale)} - ${formatTime(point.bucketEnd, locale)}: ${point.successRate.toFixed(1)}%`}
                  />
                }
              />
              <TooltipContent side='top' className='text-xs'>
                <div className='space-y-1'>
                  <div className='font-mono'>
                    {formatTime(point.bucketStart, locale)} -{' '}
                    {formatTime(point.bucketEnd, locale)}
                  </div>
                  <div>
                    <span className='text-success'>✓ {point.successCount}</span>{' '}
                    <span className='text-destructive'>
                      × {point.failureCount}
                    </span>{' '}
                    <span>({point.successRate.toFixed(1)}%)</span>
                  </div>
                  {point.ttftSampleCount > 0 && (
                    <div className='text-foreground/80'>
                      {t('First token')}: {point.ttftMs} ms
                    </div>
                  )}
                </div>
              </TooltipContent>
            </Tooltip>
          ))}
        </div>
      </TooltipProvider>
    </div>
  )
}
