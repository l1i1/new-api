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
import { RefreshCw } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

import { getAllChannelObservability } from '../../api'
import { dedupeChannelObservabilityRows } from '../../lib/channel-observability'
import type { ChannelObservabilityResult } from '../../types'
import { useChannels } from '../channels-provider'

type ChannelObservabilityDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

function formatPercent(value: number): string {
  return `${value.toFixed(1)}%`
}

function formatSampled(
  value: number,
  sufficient: boolean,
  translate: (key: string) => string
): string {
  return sufficient ? formatPercent(value) : translate('Insufficient sample')
}

function formatRangeLabel(
  value: number,
  translate: (key: string) => string
): string {
  if (value === 1) return translate('1 hour')
  if (value === 24) return translate('24 hours')
  return translate('7 days')
}

export function ChannelObservabilityDialog(
  props: ChannelObservabilityDialogProps
) {
  const { t } = useTranslation()
  const { currentRow } = useChannels()
  const [hours, setHours] = useState(24)
  const [rows, setRows] = useState<ChannelObservabilityResult[]>([])
  const [isLoading, setIsLoading] = useState(false)

  const load = async () => {
    if (!currentRow) return
    setIsLoading(true)
    try {
      const loadedRows = await getAllChannelObservability(
        currentRow.id,
        hours,
        {
          aggregate_by_model: true,
        }
      )
      setRows(dedupeChannelObservabilityRows(loadedRows))
    } catch (error: unknown) {
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    } finally {
      setIsLoading(false)
    }
  }

  useEffect(() => {
    if (props.open) void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.open, currentRow?.id, hours])

  const summary = useMemo(() => {
    const requests = rows.reduce(
      (total, row) => total + Math.max(0, row.request_count),
      0
    )
    const successes = rows.reduce((total, row) => {
      const requestCount = Math.max(0, row.request_count)
      if (Number.isFinite(row.request_success_count)) {
        return (
          total +
          Math.min(requestCount, Math.max(0, row.request_success_count ?? 0))
        )
      }
      return (
        total +
        Math.min(
          requestCount,
          Math.round(
            (requestCount *
              Math.min(100, Math.max(0, row.request_success_rate))) /
              100
          )
        )
      )
    }, 0)
    const failures = Math.max(0, requests - successes)
    return {
      requests,
      successes,
      failures,
      successRate: requests > 0 ? (successes / requests) * 100 : 0,
    }
  }, [rows])

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Channel observability')}
      description={t(
        'Requests, retries, cache behavior, p95 latency, and time to first token by model'
      )}
      contentClassName='max-w-[min(96vw,1440px)]'
      contentHeight='min(80vh, 760px)'
      bodyClassName='space-y-4'
    >
      <div className='flex items-center justify-between gap-3'>
        <div className='flex gap-2'>
          {[1, 24, 168].map((value) => (
            <Button
              key={value}
              variant={hours === value ? 'default' : 'outline'}
              size='sm'
              onClick={() => setHours(value)}
            >
              {formatRangeLabel(value, t)}
            </Button>
          ))}
        </div>
        <Button
          variant='outline'
          size='icon-sm'
          onClick={() => void load()}
          disabled={isLoading}
          aria-label={t('Refresh')}
        >
          <RefreshCw className={isLoading ? 'size-4 animate-spin' : 'size-4'} />
        </Button>
      </div>

      <div className='grid grid-cols-2 divide-x divide-y rounded-md border sm:grid-cols-4 sm:divide-y-0'>
        <SummaryMetric label={t('Requests')} value={summary.requests} />
        <SummaryMetric
          label={t('Successful requests')}
          value={summary.successes}
          tone='success'
        />
        <SummaryMetric
          label={t('Failed requests')}
          value={summary.failures}
          tone='danger'
        />
        <SummaryMetric
          label={t('Success rate')}
          value={formatPercent(summary.successRate)}
        />
      </div>

      <div className='min-h-0 overflow-y-auto rounded-md border [&_[data-slot=table-container]]:overflow-x-hidden'>
        {rows.length === 0 && !isLoading ? (
          <div className='text-muted-foreground py-12 text-center'>
            {t('No observability data available')}
          </div>
        ) : (
          <StaticDataTable
            data={rows}
            getRowKey={(row) =>
              `${row.requested_model}-${row.group}-${row.protocol}-${row.credential_id}`
            }
            tableClassName='table-fixed'
            columns={[
              {
                id: 'model',
                header: t('Model'),
                className: 'w-[22%]',
                cellClassName: 'max-w-0',
                cell: (row) => row.requested_model,
              },
              {
                id: 'requests',
                header: t('Requests'),
                className: 'w-[9%] text-right',
                cellClassName: 'text-right',
                cell: (row) => row.request_count.toLocaleString(),
              },
              {
                id: 'attempts',
                header: t('Attempts'),
                className: 'hidden w-[9%] text-right sm:table-cell',
                cellClassName: 'hidden text-right sm:table-cell',
                cell: (row) => row.attempt_count.toLocaleString(),
              },
              {
                id: 'success',
                header: t('Success rate'),
                className: 'w-[12%] text-right',
                cellClassName: 'text-right',
                cell: (row) =>
                  formatSampled(
                    row.request_success_rate,
                    row.sample_sufficient,
                    t
                  ),
              },
              {
                id: 'attempt-success',
                header: t('Attempt success'),
                className: 'hidden w-[12%] text-right lg:table-cell',
                cellClassName: 'hidden text-right lg:table-cell',
                cell: (row) =>
                  formatSampled(
                    row.attempt_success_rate,
                    row.sample_sufficient,
                    t
                  ),
              },
              {
                id: 'cache',
                header: t('Cache hit rate'),
                className: 'hidden w-[12%] text-right md:table-cell',
                cellClassName: 'hidden text-right md:table-cell',
                cell: (row) =>
                  formatSampled(row.cache_hit_rate, row.usage_sufficient, t),
              },
              {
                id: 'latency',
                header: t('P95 latency'),
                className: 'w-[14%] text-right',
                cellClassName: 'text-right',
                cell: (row) =>
                  row.sample_sufficient
                    ? `${row.p95_latency_ms} ms`
                    : t('Insufficient sample'),
              },
              {
                id: 'ttft',
                header: t('P95 first token'),
                className: 'hidden w-[14%] text-right md:table-cell',
                cellClassName: 'hidden text-right md:table-cell',
                cell: (row) =>
                  row.sample_sufficient
                    ? `${row.p95_ttft_ms} ms`
                    : t('Insufficient sample'),
              },
              {
                id: 'coverage',
                header: t('Coverage'),
                className: 'hidden w-[10%] text-right lg:table-cell',
                cellClassName: 'hidden text-right lg:table-cell',
                cell: (row) => formatPercent(row.sample_coverage),
              },
            ]}
          />
        )}
      </div>
    </Dialog>
  )
}

function SummaryMetric(props: {
  label: string
  value: string | number
  tone?: 'success' | 'danger'
}) {
  let valueClassName = 'text-lg font-semibold'
  if (props.tone === 'success') {
    valueClassName = 'text-success text-lg font-semibold'
  } else if (props.tone === 'danger') {
    valueClassName = 'text-destructive text-lg font-semibold'
  }

  return (
    <div className='px-3 py-2'>
      <div className='text-muted-foreground text-xs'>{props.label}</div>
      <div className={valueClassName}>
        {typeof props.value === 'number'
          ? props.value.toLocaleString()
          : props.value}
      </div>
    </div>
  )
}
