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
import { ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

import type {
  ObservabilityContinuation,
  ObservabilityFit,
} from '../lib/observability'

/**
 * Transparent-continuation marker. Reuses the existing StatusBadge language:
 * a neutral pill in the Stream cell (desktop) and inline on the mobile card.
 */
export function ContinuationBadge(props: {
  continuation: ObservabilityContinuation
  className?: string
  /**
   * Desktop cells expose the audit detail in a tooltip; touch surfaces (the
   * mobile card) pass false because tooltips are not reachable there.
   */
  withTooltip?: boolean
}) {
  const badge = (
    <StatusBadge
      label={props.continuation.badge}
      variant='neutral'
      size='sm'
      showDot={false}
      copyable={false}
      className={cn('font-mono tabular-nums', props.className)}
    />
  )
  if (!props.withTooltip) return badge
  return (
    <TooltipProvider delay={300}>
      <Tooltip>
        <TooltipTrigger render={<span className='inline-flex w-fit' />}>
          {badge}
        </TooltipTrigger>
        <TooltipContent side='top'>
          <div className='space-y-0.5 text-xs'>
            {props.continuation.tooltipRows.map((row) => (
              <p key={row.label}>
                <span className='text-muted-foreground'>{row.label}: </span>
                <span className='font-mono tabular-nums'>{row.value}</span>
              </p>
            ))}
          </div>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

/**
 * Fit-capability pin of the serving channel. The popover lists the fit family,
 * its behaviour marks and rules; the policy version and baseline hash stay in
 * the small mono footer so the main list stays readable.
 */
export function FitPinPopover(props: { fit: ObservabilityFit }) {
  const { t } = useTranslation()
  const footer = [
    props.fit.policy != null ? `${t('Fit Policy')} ${props.fit.policy}` : null,
    props.fit.baseline ? `${t('Baseline')} ${props.fit.baseline}` : null,
  ]
    .filter(Boolean)
    .join(' · ')

  return (
    <Popover>
      <PopoverTrigger
        render={
          <button
            type='button'
            className='focus-visible:ring-ring inline-flex size-5 shrink-0 items-center justify-center rounded-full text-emerald-600 transition-colors hover:text-emerald-500 focus-visible:ring-2 focus-visible:outline-none dark:text-emerald-400'
            aria-label={t('Fit Pin')}
            onClick={(event) => event.stopPropagation()}
          />
        }
      >
        <ShieldCheck className='size-3.5' aria-hidden='true' />
      </PopoverTrigger>
      <PopoverContent side='top' align='start' className='w-64 text-xs'>
        <div className='flex flex-col gap-1.5'>
          <p className='flex items-center gap-1.5 font-medium'>
            <ShieldCheck
              className='size-3.5 shrink-0 text-emerald-600 dark:text-emerald-400'
              aria-hidden='true'
            />
            {t('Fit Pin')}
          </p>
          {props.fit.rows.map((row) => (
            <div key={row.label} className='flex min-w-0 gap-2'>
              <span className='text-muted-foreground shrink-0'>
                {row.label}
              </span>
              <span className='min-w-0 font-mono break-all'>{row.value}</span>
            </div>
          ))}
          {footer && (
            <p className='text-muted-foreground/70 border-t pt-1 font-mono text-[11px] break-all'>
              {footer}
            </p>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}
