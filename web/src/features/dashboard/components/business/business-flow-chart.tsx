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
import { VChart } from '@visactor/react-vchart'
import { Activity } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { IconBadge } from '@/components/ui/icon-badge'
import { PanelWrapper } from '@/features/dashboard/components/ui/panel-wrapper'
import type {
  BusinessAnalysisReport,
  BusinessFlowBucket,
} from '@/features/dashboard/types'
import { useChartTheme } from '@/lib/use-chart-theme'
import { VCHART_OPTION } from '@/lib/vchart'
import type { DisplayCurrency } from '@/stores/currency-display-store'

interface BusinessFlowChartProps {
  rows: BusinessFlowBucket[]
  report: BusinessAnalysisReport
  displayCurrency: DisplayCurrency
  loading?: boolean
}

function quotaToUSD(quota: number, report: BusinessAnalysisReport): number {
  if (report.quota_per_unit <= 0) return 0
  return quota / report.quota_per_unit
}

function formatMoney(value: number, currency: DisplayCurrency): string {
  const symbol = currency === 'CNY' ? '¥' : '$'
  return `${symbol}${value.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`
}

export function BusinessFlowChart(props: BusinessFlowChartProps) {
  const { t } = useTranslation()
  const { resolvedTheme, themeReady } = useChartTheme()
  const spec = useMemo(() => {
    const values = props.rows.flatMap((row) => [
      {
        period: row.label,
        metric: t('Top-ups'),
        value:
          props.displayCurrency === 'CNY'
            ? row.topup_cny
            : row.topup_cny / props.report.cny_per_usd,
      },
      {
        period: row.label,
        metric: t('Consumption'),
        value:
          quotaToUSD(row.consume_quota, props.report) *
          (props.displayCurrency === 'CNY' ? props.report.cny_per_usd : 1),
      },
      {
        period: row.label,
        metric: t('Non-recharge increase'),
        value:
          quotaToUSD(row.non_recharge_increase_quota, props.report) *
          (props.displayCurrency === 'CNY' ? props.report.cny_per_usd : 1),
      },
      {
        period: row.label,
        metric: t('Net change'),
        value:
          quotaToUSD(row.net_after_consume_quota, props.report) *
          (props.displayCurrency === 'CNY' ? props.report.cny_per_usd : 1),
      },
    ])

    return {
      type: 'bar',
      data: [{ id: 'business-flow', values }],
      xField: 'period',
      yField: 'value',
      seriesField: 'metric',
      stack: false,
      legends: { visible: true, selectMode: 'multiple' },
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => String(datum.metric),
              value: (datum: Record<string, unknown>) =>
                formatMoney(Number(datum.value) || 0, props.displayCurrency),
            },
          ],
        },
      },
      background: { fill: 'transparent' },
      animation: true,
    }
  }, [props.displayCurrency, props.report, props.rows, t])

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='chart-4' size='sm'>
            <Activity />
          </IconBadge>
          <span>{t('Operating flow')}</span>
        </span>
      }
      description={t('Top-ups, consumption, grants, and net change by period.')}
      loading={props.loading}
      empty={!props.loading && props.rows.length === 0}
      height='h-[300px] sm:h-[360px]'
      contentClassName='p-1.5 sm:p-2'
    >
      <div className='h-[292px] sm:h-[352px]'>
        {themeReady && (
          <VChart
            key={`${resolvedTheme}-${props.rows.length}`}
            spec={{
              ...spec,
              theme: resolvedTheme === 'dark' ? 'dark' : 'light',
            }}
            option={VCHART_OPTION}
          />
        )}
      </div>
    </PanelWrapper>
  )
}
