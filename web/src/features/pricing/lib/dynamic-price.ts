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
import { TOKEN_UNIT_DIVISORS } from '../constants'
import type { PricingCurrency, PricingModel, TokenUnit } from '../types'
import {
  BILLING_PRICING_VARS,
  parseTiersFromExpr,
  splitBillingExprAndRequestRules,
  tryParseRequestRuleExpr,
  type BillingVar,
  type ParsedTier,
} from './billing-expr'
import { getDisplayGroupRatio } from './model-helpers'
import { formatPricingCurrencyFromUSD } from './price'
import { evalExprLocally } from './tier-expr'

type DynamicPriceOptions = {
  tokenUnit: TokenUnit
  showRechargePrice?: boolean
  priceRate?: number
  usdExchangeRate?: number
  displayCurrency?: PricingCurrency
  groupRatioMultiplier?: number
  now?: Date
}

export type DynamicPriceEntry = {
  key: string
  field: string
  label: string
  shortLabel: string
  value: number
  formatted: string
  /** Undiscounted price shown struck through when a group ratio applies. */
  original?: string
  variable: BillingVar
}

export type DynamicPricingSummary = {
  tiers: ParsedTier[]
  tier: ParsedTier | null
  tierCount: number
  hasRequestRules: boolean
  isSpecialExpression: boolean
  rawExpression: string
  entries: DynamicPriceEntry[]
  primaryEntries: DynamicPriceEntry[]
  secondaryEntries: DynamicPriceEntry[]
}

const PRIMARY_DYNAMIC_FIELDS = new Set(['inputPrice', 'outputPrice'])
const TIME_FUNCTION_PATTERN = /\b(?:hour|minute|weekday|month|day)\s*\(/u
const EMPTY_EXTRA_TOKEN_VALUES = {
  cacheReadTokens: 0,
  cacheCreateTokens: 0,
  cacheCreate1hTokens: 0,
  imageTokens: 0,
  imageOutputTokens: 0,
  audioInputTokens: 0,
  audioOutputTokens: 0,
}

export function isDynamicPricingModel(model: PricingModel): boolean {
  return model.billing_mode === 'tiered_expr' && Boolean(model.billing_expr)
}

export function getDynamicDisplayGroupRatio(
  model: PricingModel,
  selectedGroup?: string
): number {
  return getDisplayGroupRatio(model, selectedGroup)
}

function applyRechargeRate(
  price: number,
  showWithRecharge: boolean,
  priceRate: number,
  usdExchangeRate: number
): number {
  if (!showWithRecharge) return price
  return (price * priceRate) / usdExchangeRate
}

export function formatDynamicUnitPrice(
  valuePerMillionTokens: number,
  options: DynamicPriceOptions
): string {
  const groupRatio = options.groupRatioMultiplier ?? 1
  const priceRate = options.priceRate ?? 1
  const usdExchangeRate = options.usdExchangeRate ?? 1
  const priceUSD =
    (valuePerMillionTokens * groupRatio) /
    TOKEN_UNIT_DIVISORS[options.tokenUnit]
  const displayPrice = applyRechargeRate(
    priceUSD,
    options.showRechargePrice ?? false,
    priceRate,
    usdExchangeRate
  )

  return formatPricingCurrencyFromUSD(
    displayPrice,
    options.displayCurrency ?? 'CNY',
    usdExchangeRate,
    { digitsLarge: 4, digitsSmall: 6 }
  )
}

export function getDynamicPricingTiers(model: PricingModel): ParsedTier[] {
  if (!isDynamicPricingModel(model)) return []
  const { billingExpr } = splitBillingExprAndRequestRules(
    model.billing_expr || ''
  )
  return parseTiersFromExpr(billingExpr)
}

function getDisplayedTier(
  tiers: ParsedTier[],
  billingExpr: string,
  now: Date | undefined
): ParsedTier | null {
  const firstTier = tiers[0] || null
  if (!firstTier || !TIME_FUNCTION_PATTERN.test(billingExpr)) {
    return firstTier
  }

  const expression = billingExpr.replace(/^v\d+:/u, '')
  const result = evalExprLocally(
    expression,
    0,
    0,
    EMPTY_EXTRA_TOKEN_VALUES,
    now
  )
  if (result.error || !result.matchedTier) return firstTier
  return tiers.find((tier) => tier.label === result.matchedTier) || firstTier
}

export function hasDynamicRequestRules(model: PricingModel): boolean {
  if (!isDynamicPricingModel(model)) return false
  const { requestRuleExpr } = splitBillingExprAndRequestRules(
    model.billing_expr || ''
  )
  return Boolean(tryParseRequestRuleExpr(requestRuleExpr || '')?.length)
}

export function getDynamicPriceEntries(
  tier: ParsedTier | null,
  options: DynamicPriceOptions
): DynamicPriceEntry[] {
  if (!tier) return []

  const groupRatio = options.groupRatioMultiplier ?? 1

  return BILLING_PRICING_VARS.flatMap((variable) => {
    if (!variable.field) return []
    const value = Number(tier[variable.field])
    if (!Number.isFinite(value) || value <= 0) return []

    return [
      {
        key: variable.key,
        field: variable.field,
        label: variable.label,
        shortLabel: variable.shortLabel,
        value,
        formatted: formatDynamicUnitPrice(value, options),
        original:
          groupRatio === 1
            ? undefined
            : formatDynamicUnitPrice(value, {
                ...options,
                groupRatioMultiplier: 1,
              }),
        variable,
      },
    ]
  }).sort((a, b) => {
    const aPrimary = PRIMARY_DYNAMIC_FIELDS.has(a.field)
    const bPrimary = PRIMARY_DYNAMIC_FIELDS.has(b.field)
    if (aPrimary !== bPrimary) return aPrimary ? -1 : 1
    return 0
  })
}

export function getDynamicPricingSummary(
  model: PricingModel,
  options: DynamicPriceOptions
): DynamicPricingSummary | null {
  if (!isDynamicPricingModel(model)) return null

  const { billingExpr } = splitBillingExprAndRequestRules(
    model.billing_expr || ''
  )
  const tiers = parseTiersFromExpr(billingExpr)
  const tier = getDisplayedTier(tiers, billingExpr, options.now)
  const entries = getDynamicPriceEntries(tier, options)
  const rawExpression = model.billing_expr || ''

  return {
    tiers,
    tier,
    tierCount: tiers.length,
    hasRequestRules: hasDynamicRequestRules(model),
    isSpecialExpression: rawExpression.trim().length > 0 && tiers.length === 0,
    rawExpression,
    entries,
    primaryEntries: entries.filter((entry) =>
      PRIMARY_DYNAMIC_FIELDS.has(entry.field)
    ),
    secondaryEntries: entries.filter(
      (entry) => !PRIMARY_DYNAMIC_FIELDS.has(entry.field)
    ),
  }
}
