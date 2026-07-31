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
import { AlertTriangle, Database } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { IconBadge } from '@/components/ui/icon-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { PanelWrapper } from '@/features/dashboard/components/ui/panel-wrapper'
import type {
  BusinessAnalysisReport,
  BusinessBalanceRow,
} from '@/features/dashboard/types'

import { formatPercent, formatQuota, formatQuotaMoney } from './business-format'

interface BusinessInventoryProps {
  report: BusinessAnalysisReport
  loading?: boolean
}

function BalanceTable(props: {
  rows: BusinessBalanceRow[]
  report: BusinessAnalysisReport
  emptyMessage: string
}) {
  const { t } = useTranslation()
  if (props.rows.length === 0) {
    return (
      <div className='text-muted-foreground py-8 text-center text-sm'>
        {props.emptyMessage}
      </div>
    )
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('Account')}</TableHead>
          <TableHead className='text-right'>{t('Visible balance')}</TableHead>
          <TableHead className='hidden text-right sm:table-cell'>
            {t('Used')}
          </TableHead>
          <TableHead className='hidden text-right md:table-cell'>
            {t('Requests')}
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {props.rows.map((row) => (
          <TableRow key={row.id}>
            <TableCell>
              <div className='flex min-w-28 flex-col'>
                <span className='font-medium'>{row.username}</span>
                <span className='text-muted-foreground text-xs'>#{row.id}</span>
              </div>
            </TableCell>
            <TableCell className='text-right font-medium'>
              <div>{formatQuotaMoney(row.visible, props.report)}</div>
              <div className='text-muted-foreground text-xs'>
                {t('{{quota}} quota', { quota: formatQuota(row.visible) })}
              </div>
            </TableCell>
            <TableCell className='hidden text-right sm:table-cell'>
              {formatQuota(row.used_quota)}
            </TableCell>
            <TableCell className='hidden text-right md:table-cell'>
              {formatQuota(row.request_count)}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

export function BusinessInventory(props: BusinessInventoryProps) {
  const { t } = useTranslation()
  const inventory = props.report.inventory
  const origin = props.report.quota_origin

  return (
    <div className='grid gap-3 lg:grid-cols-2'>
      <PanelWrapper
        title={
          <span className='flex items-center gap-2'>
            <IconBadge tone='chart-2' size='sm'>
              <Database />
            </IconBadge>
            <span>{t('Current quota inventory')}</span>
          </span>
        }
        description={t('Enabled account balances available for future use.')}
        loading={props.loading}
      >
        <div className='grid grid-cols-2 gap-2 sm:grid-cols-3'>
          <Metric
            label={t('Stocking balance')}
            value={formatQuotaMoney(
              inventory.consumable_enabled_visible,
              props.report
            )}
          />
          <Metric
            label={t('Ordinary balance')}
            value={formatQuotaMoney(
              inventory.consumable_enabled_quota_only,
              props.report
            )}
          />
          <Metric
            label={t('Invite balance')}
            value={formatQuotaMoney(
              inventory.consumable_enabled_aff_only,
              props.report
            )}
          />
          <Metric
            label={t('Enabled net')}
            value={formatQuotaMoney(
              inventory.net_enabled_visible,
              props.report
            )}
          />
          <Metric
            label={t('Disabled / deleted')}
            value={formatQuotaMoney(
              inventory.disabled_or_deleted_positive_visible,
              props.report
            )}
          />
          <Metric
            label={t('Accounts')}
            value={inventory.users.enabled.toLocaleString()}
          />
        </div>
        <div className='mt-4 border-t pt-3'>
          <div className='mb-2 flex items-center justify-between text-xs'>
            <span className='text-muted-foreground'>
              {t('Balance concentration')}
            </span>
            <span className='text-muted-foreground'>{t('Top 1 / 5 / 20')}</span>
          </div>
          <div className='grid grid-cols-3 gap-2'>
            <Metric
              label={t('Top 1')}
              value={formatPercent(inventory.concentration.top1)}
            />
            <Metric
              label={t('Top 5')}
              value={formatPercent(inventory.concentration.top5)}
            />
            <Metric
              label={t('Top 20')}
              value={formatPercent(inventory.concentration.top20)}
            />
          </div>
        </div>
        <div className='mt-4 border-t pt-3'>
          <div className='mb-2 text-sm font-semibold'>
            {t('Largest enabled balances')}
          </div>
          <BalanceTable
            rows={inventory.top20}
            report={props.report}
            emptyMessage={t('No enabled account has a positive balance.')}
          />
        </div>
      </PanelWrapper>

      <PanelWrapper
        title={
          <span className='flex items-center gap-2'>
            <IconBadge tone='warning' size='sm'>
              <AlertTriangle />
            </IconBadge>
            <span>{t('Ordinary balance origin')}</span>
          </span>
        }
        description={t('Accounts with ordinary quota but no completed top-up.')}
        loading={props.loading}
      >
        <div className='grid grid-cols-2 gap-2 sm:grid-cols-3'>
          <Metric
            label={t('With top-up')}
            value={`${origin.positive_quota_with_topup_users.toLocaleString()} · ${formatQuotaMoney(origin.positive_quota_with_topup_total, props.report)}`}
          />
          <Metric
            label={t('Without top-up')}
            value={`${origin.positive_quota_no_topup_users.toLocaleString()} · ${formatQuotaMoney(origin.positive_quota_no_topup_total, props.report)}`}
          />
          <Metric
            label={t('Invite quota')}
            value={`${origin.enabled_positive_aff_users.toLocaleString()} · ${formatQuotaMoney(origin.enabled_positive_aff_total, props.report)}`}
          />
          <Metric
            label={t('New-user grant')}
            value={formatQuotaMoney(
              origin.options.quota_for_new_user,
              props.report
            )}
          />
          <Metric
            label={t('Check-in range')}
            value={`${formatQuota(origin.options.checkin_min_quota)} - ${formatQuota(origin.options.checkin_max_quota)}`}
          />
          <Metric
            label={t('Enabled users')}
            value={origin.enabled_users.toLocaleString()}
          />
        </div>
        <div className='mt-4 border-t pt-3'>
          <div className='mb-2 text-sm font-semibold'>
            {t('No-top-up accounts')}
          </div>
          <BalanceTable
            rows={origin.top_no_topup}
            report={props.report}
            emptyMessage={t(
              'All positive ordinary balances have a completed top-up.'
            )}
          />
        </div>
      </PanelWrapper>
    </div>
  )
}

function Metric(props: { label: string; value: string }) {
  return (
    <div className='bg-muted/40 min-w-0 rounded-lg border border-transparent px-2.5 py-2'>
      <div className='text-muted-foreground truncate text-[11px] font-medium'>
        {props.label}
      </div>
      <div
        className='mt-1 truncate text-xs font-semibold tabular-nums'
        title={props.value}
      >
        {props.value}
      </div>
    </div>
  )
}
