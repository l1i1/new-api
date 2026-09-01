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
import { ChevronRight, Copy } from 'lucide-react'
import { memo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { getLobeIcon } from '@/lib/lobe-icon'
import { resolveTntContent } from '@/lib/tnt-content'
import { cn } from '@/lib/utils'

import { DEFAULT_TOKEN_UNIT } from '../constants'
import {
  getCardExamplePrice,
  getDynamicDisplayGroupRatio,
  getDynamicPriceUnitLabelKey,
  getDynamicPricingSummary,
  isUnconfiguredTaskUsageModel,
} from '../lib/dynamic-price'
import { getTaskNumberFields } from '../lib/task-expr'
import { parseTags } from '../lib/filters'
import { isTokenBasedModel } from '../lib/model-helpers'
import { formatPriceParts, formatRequestPriceParts } from '../lib/price'
import { localizePricingVendorName } from '../lib/vendor-localization'
import type { PricingCurrency, PricingModel, TokenUnit } from '../types'
import { ModelPerfBadge, type ModelPerfBadgeData } from './model-perf-badge'

export interface ModelCardProps {
  model: PricingModel
  onClick: () => void
  priceRate?: number
  usdExchangeRate?: number
  tokenUnit?: TokenUnit
  showRechargePrice?: boolean
  displayCurrency?: PricingCurrency
  selectedGroup?: string
  perf?: ModelPerfBadgeData
}

function PriceRow(props: {
  label: string
  value: string
  original?: string
  unit?: string
}) {
  return (
    <div className='flex items-baseline gap-1.5 text-xs'>
      <span className='text-muted-foreground shrink-0'>{props.label}</span>
      {props.original && (
        <span className='text-muted-foreground/60 font-mono text-xs tabular-nums line-through'>
          {props.original}
        </span>
      )}
      <span className='text-foreground font-mono text-sm font-semibold tabular-nums'>
        {props.value}
      </span>
      {props.unit && (
        <span className='text-muted-foreground/70 shrink-0'>{props.unit}</span>
      )}
    </div>
  )
}

export const ModelCard = memo(function ModelCard(props: ModelCardProps) {
  const { i18n, t } = useTranslation()
  const { copyToClipboard } = useCopyToClipboard()
  const tokenUnit = props.tokenUnit ?? DEFAULT_TOKEN_UNIT
  const priceRate = props.priceRate ?? 1
  const usdExchangeRate = props.usdExchangeRate ?? 1
  const showRechargePrice = props.showRechargePrice ?? false
  const displayCurrency = props.displayCurrency ?? 'CNY'
  const isTokenBased = isTokenBasedModel(props.model)
  const tokenUnitLabel = tokenUnit === 'K' ? '1K' : '1M'
  const tags = parseTags(props.model.localized_tags)
  const groups = props.model.enable_groups || []
  const vendorLabel = props.model.vendor_name
    ? localizePricingVendorName(
        props.model.vendor_name,
        props.model.vendor_display_name,
        i18n.resolvedLanguage || i18n.language
      )
    : undefined
  const chips = [vendorLabel, ...tags]
    .filter((chip): chip is string => Boolean(chip))
    .slice(0, 4)
  const modelIconKey = props.model.icon || props.model.vendor_icon
  const modelIcon = modelIconKey ? getLobeIcon(modelIconKey, 40) : null
  const initial = props.model.model_name?.charAt(0).toUpperCase() || '?'
  const isDynamicPricing =
    props.model.billing_mode === 'tiered_expr' &&
    Boolean(props.model.billing_expr)
  const isUnconfiguredTaskUsage = isUnconfiguredTaskUsageModel(props.model)
  const hasCachedPrice = isTokenBased && props.model.cache_ratio != null
  const dynamicPriceOptions = {
    tokenUnit,
    showRechargePrice,
    priceRate,
    usdExchangeRate,
    groupRatioMultiplier: getDynamicDisplayGroupRatio(
      props.model,
      props.selectedGroup
    ),
  }
  const dynamicSummary = isDynamicPricing
    ? getDynamicPricingSummary(props.model, {
        tokenUnit,
        showRechargePrice,
        priceRate,
        usdExchangeRate,
        displayCurrency,
        groupRatioMultiplier: getDynamicDisplayGroupRatio(
          props.model,
          props.selectedGroup
        ),
      })
    : null
  const description = resolveTntContent(
    props.model.description || '',
    i18n.resolvedLanguage || i18n.language
  )

  let billingModeLabel = t('Per Request')
  if (isDynamicPricing) {
    billingModeLabel = t('Dynamic Pricing')
  } else if (isTokenBased) {
    billingModeLabel = t('Token-based')
  }
  const metaLine = [
    ...groups.slice(0, 1),
    billingModeLabel,
    isTokenBased ? `${tokenUnitLabel} tokens` : undefined,
  ]
    .filter(Boolean)
    .join(' · ')

  const handleCopy = (e: React.MouseEvent) => {
    e.stopPropagation()
    copyToClipboard(props.model.model_name || '')
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      props.onClick()
    }
  }

  let priceContent: ReactNode
  if (dynamicSummary) {
    if (dynamicSummary.isSpecialExpression) {
      priceContent = (
        <div className='col-span-2 min-w-0'>
          <span className='text-amber-700 dark:text-amber-300'>
            {t('Special billing expression')}
          </span>
          <code className='text-muted-foreground/70 mt-0.5 line-clamp-1 block font-mono text-[11px] break-all'>
            {dynamicSummary.rawExpression}
          </code>
        </div>
      )
    } else if (dynamicSummary.primaryEntries.length > 0) {
      priceContent = dynamicSummary.primaryEntries.map((entry) => (
        <PriceRow
          key={entry.key}
          label={t(entry.shortLabel)}
          value={entry.formatted}
          original={entry.original}
        />
      ))
    } else {
      priceContent = (
        <span className='text-muted-foreground col-span-2 text-xs'>
          {t('Dynamic Pricing')}
        </span>
      )
    }
  } else if (isUnconfiguredTaskUsage) {
    priceSummary = (
      <span className='text-muted-foreground text-sm'>
        {t('Usage-based billing · price not configured')}
      </span>
    )
  } else if (isTokenBased) {
    const inputParts = formatPriceParts(
      props.model,
      'input',
      tokenUnit,
      showRechargePrice,
      priceRate,
      usdExchangeRate,
      props.selectedGroup,
      displayCurrency
    )
    const outputParts = formatPriceParts(
      props.model,
      'output',
      tokenUnit,
      showRechargePrice,
      priceRate,
      usdExchangeRate,
      props.selectedGroup,
      displayCurrency
    )
    const cacheParts = hasCachedPrice
      ? formatPriceParts(
          props.model,
          'cache',
          tokenUnit,
          showRechargePrice,
          priceRate,
          usdExchangeRate,
          props.selectedGroup,
          displayCurrency
        )
      : null
    priceContent = (
      <>
        <PriceRow
          label={t('Input')}
          value={inputParts.price}
          original={inputParts.original}
        />
        <PriceRow
          label={t('Output')}
          value={outputParts.price}
          original={outputParts.original}
        />
        {cacheParts && (
          <PriceRow
            label={t('Cached')}
            value={cacheParts.price}
            original={cacheParts.original}
          />
        )}
      </>
    )
  } else {
    const requestParts = formatRequestPriceParts(
      props.model,
      showRechargePrice,
      priceRate,
      usdExchangeRate,
      props.selectedGroup,
      displayCurrency
    )
    priceContent = (
      <PriceRow
        label={t('Price')}
        value={requestParts.price}
        original={requestParts.original}
        unit={`/ ${t('request')}`}
      />
    )
  }

  return (
    <div
      role='button'
      tabIndex={0}
      onClick={props.onClick}
      onKeyDown={handleKeyDown}
      className={cn(
        'group border-border hover:bg-muted/30 -mt-px -ml-px flex cursor-pointer flex-col gap-3 border p-4 transition-colors outline-none sm:p-5',
        'focus-visible:ring-ring focus-visible:z-10 focus-visible:ring-2 focus-visible:ring-inset'
      )}
    >
      <div className='flex items-start gap-3'>
        <span className='flex size-10 shrink-0 items-center justify-center overflow-hidden'>
          {modelIcon || (
            <span className='text-muted-foreground text-lg font-semibold'>
              {initial}
            </span>
          )}
        </span>
        <div className='min-w-0 flex-1'>
          <h3
            className='text-foreground truncate font-mono text-base font-semibold'
            title={props.model.model_name}
          >
            {props.model.model_name}
          </h3>
          {chips.length > 0 && (
            <div className='mt-1.5 flex flex-wrap gap-1.5'>
              {chips.map((chip) => (
                <span
                  key={chip}
                  className='bg-muted/70 text-muted-foreground rounded-sm px-1.5 py-0.5 text-[11px] leading-tight'
                >
                  {chip}
                </span>
              ))}
            </div>
          )}
        </div>
        <div className='flex shrink-0 items-center gap-1.5'>
          <button
            type='button'
            onClick={props.onClick}
            className='text-muted-foreground hover:text-foreground hover:bg-muted focus-visible:ring-ring inline-flex items-center gap-1 rounded-none border px-2 py-1 text-xs transition-colors outline-none focus-visible:ring-2'
          >
            {t('Details')}
            <ChevronRight className='size-3.5' aria-hidden='true' />
          </button>
          <button
            type='button'
            onClick={handleCopy}
            className='text-muted-foreground hover:text-foreground hover:bg-muted focus-visible:ring-ring rounded-none border p-1.5 transition-colors outline-none focus-visible:ring-2'
            title={t('Copy')}
            aria-label={t('Copy')}
          >
            <Copy className='size-3.5' />
          </button>
        </div>
      </div>

      <p className='text-muted-foreground line-clamp-2 min-h-10 text-sm leading-relaxed'>
        {description || t('No description available.')}
      </p>

      <div className='mt-auto grid grid-cols-2 gap-x-3 gap-y-1'>
        {priceContent}
      </div>

      <div className='border-border/60 flex items-end justify-between gap-2 border-t pt-2'>
        <div className='text-muted-foreground/70 min-w-0 truncate text-xs'>
          {metaLine}
        </div>
        <ModelPerfBadge perf={props.perf} className='shrink-0' />
      </div>
    </div>
  )
})
