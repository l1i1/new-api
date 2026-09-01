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
import { formatBillingCurrencyFromUSD } from '@/lib/currency'

import { TOKEN_UNIT_DIVISORS } from '../constants'
import type {
  BillingUsageSchema,
  BillingUsageUnit,
  PricingCurrency,
  PricingModel,
  TokenUnit,
} from '../types'
import {
  BILLING_PRICING_VARS,
  parseTaskTiersFromExpr,
  parseTiersFromExpr,
  splitBillingExprAndRequestRules,
  tryParseRequestRuleExpr,
  type BillingVar,
  type ParsedTaskTier,
  type ParsedTier,
} from './billing-expr'
import { getDisplayGroupRatio } from './model-helpers'
import { formatPricingCurrencyFromUSD } from './price'
import {
  evaluateTaskVisualConfig,
  getTaskNumberFields,
  tryParseTaskVisualConfig,
} from './task-expr'
import { evalExprLocally } from './tier-expr'

export type DynamicPriceOptions = {
  tokenUnit: TokenUnit
  showRechargePrice?: boolean
  priceRate?: number
  usdExchangeRate?: number
  displayCurrency?: PricingCurrency
  groupRatioMultiplier?: number
  now?: Date
  usageSchema?: BillingUsageSchema | null
}

export type DynamicPriceLabelKind = 'i18n' | 'schema'

export type DynamicPriceEntry = {
  key: string
  field: string
  label: string
  shortLabel: string
  /** `schema` labels are raw usage-field names and must not go through `t()`. */
  labelKind: DynamicPriceLabelKind
  value: number
  formatted: string
  formattedRange?: string
  unit: BillingUsageUnit | 'request'
  /** Undiscounted price shown struck through when a group ratio applies. */
  original?: string
  variable?: BillingVar
}

export type CardExamplePrice = {
  label: string
  formatted: string
}

export type DynamicPricingTier = ParsedTier | ParsedTaskTier

export type DynamicPricingSummary = {
  tiers: DynamicPricingTier[]
  tier: DynamicPricingTier | null
  tierCount: number
  hasRequestRules: boolean
  isSpecialExpression: boolean
  rawExpression: string
  entries: DynamicPriceEntry[]
  primaryEntries: DynamicPriceEntry[]
  secondaryEntries: DynamicPriceEntry[]
  isTaskUsage: boolean
}

export function getTaskUsageQuantityUnitLabelKey(
  unit: BillingUsageUnit | undefined
): string {
  if (unit === 'second') return 's'
  if (unit === 'token') return 'token (unit)'
  if (unit === 'credit') return 'credit'
  return 'unit'
}

export function getTaskUsagePriceUnitLabelKey(
  unit: BillingUsageUnit | undefined
): string {
  if (unit === 'second') return 'second'
  if (unit === 'token') return '1M token'
  if (unit === 'credit') return 'credit'
  return 'unit'
}

export function getDynamicPriceUnitLabelKey(
  entry: DynamicPriceEntry
): string | null {
  if (entry.unit === 'second') return 's'
  if (entry.unit === 'count') return 'unit'
  if (entry.unit === 'credit') return 'credit'
  // Chat token entries also use unit 'token' but keep the 1M-token label.
  if (entry.unit === 'token' && !entry.variable) return '1M token'
  if (entry.unit === 'request') return 'request'
  return null
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

function isTaskPricingTier(tier: DynamicPricingTier): tier is ParsedTaskTier {
  return (
    Object.hasOwn(tier, 'unitPrices') &&
    typeof (tier as ParsedTaskTier).unitPrices === 'object'
  )
}

export function isDynamicPricingModel(model: PricingModel): boolean {
  return model.billing_mode === 'tiered_expr' && Boolean(model.billing_expr)
}

export function hasTaskUsageSchema(model: PricingModel): boolean {
  return Object.keys(model.billing_usage_schema ?? {}).length > 0
}

export function isTaskUsagePricingModel(model: PricingModel): boolean {
  return model.billing_mode === 'tiered_expr' && hasTaskUsageSchema(model)
}

export function isUnconfiguredTaskUsageModel(model: PricingModel): boolean {
  return (
    model.quota_type !== 1 &&
    hasTaskUsageSchema(model) &&
    !isDynamicPricingModel(model)
  )
}

export function getTaskPricingUnit(
  model: PricingModel
): BillingUsageUnit | null {
  const primaryField = getTaskNumberFields(model.billing_usage_schema)[0]
  return primaryField?.[1].unit ?? null
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

export function formatTaskUsageUnitPrice(
  valuePerUnit: number,
  options: DynamicPriceOptions
): string {
  const groupRatio = options.groupRatioMultiplier ?? 1
  const priceRate = options.priceRate ?? 1
  const usdExchangeRate = options.usdExchangeRate ?? 1
  const priceUSD = valuePerUnit * groupRatio
  const displayPrice = applyRechargeRate(
    priceUSD,
    options.showRechargePrice ?? false,
    priceRate,
    usdExchangeRate
  )

  if (!options.displayCurrency) {
    return formatBillingCurrencyFromUSD(displayPrice, {
      digitsLarge: 4,
      digitsSmall: 6,
      abbreviate: false,
    })
  }
  return formatPricingCurrencyFromUSD(
    displayPrice,
    options.displayCurrency,
    usdExchangeRate,
    { digitsLarge: 4, digitsSmall: 6 }
  )
}

export function getDynamicPricingTiers(
  model: PricingModel
): DynamicPricingTier[] {
  if (!isDynamicPricingModel(model)) return []
  const { billingExpr } = splitBillingExprAndRequestRules(
    model.billing_expr || ''
  )
  if (isTaskUsagePricingModel(model)) {
    return parseTaskTiersFromExpr(billingExpr, model.billing_usage_schema)
  }
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
  tier: DynamicPricingTier | null,
  options: DynamicPriceOptions
): DynamicPriceEntry[] {
  if (!tier) return []

  const groupRatio = options.groupRatioMultiplier ?? 1
  if (isTaskPricingTier(tier)) {
    const schema = options.usageSchema
    if (!schema) return []
    const taskTiers = options.usageSchema
      ? getTaskNumberFields(options.usageSchema)
      : []
    const entries: DynamicPriceEntry[] = taskTiers.flatMap(
      ([field, definition]) => {
        const value = Number(tier.unitPrices[field] || 0)
        if (!Number.isFinite(value) || value <= 0 || !definition.unit) return []
        return [
          {
            key: field,
            field,
            label: field,
            shortLabel: field,
            labelKind: 'schema' as const,
            value,
            formatted: formatTaskUsageUnitPrice(value, options),
            original:
              groupRatio === 1
                ? undefined
                : formatTaskUsageUnitPrice(value, {
                    ...options,
                    groupRatioMultiplier: 1,
                  }),
            unit: definition.unit,
          },
        ]
      }
    )
    if (tier.constant > 0) {
      entries.push({
        key: 'constant',
        field: 'constant',
        label: 'Base charge',
        shortLabel: 'Base',
        labelKind: 'i18n' as const,
        value: tier.constant,
        formatted: formatTaskUsageUnitPrice(tier.constant, options),
        original:
          groupRatio === 1
            ? undefined
            : formatTaskUsageUnitPrice(tier.constant, {
                ...options,
                groupRatioMultiplier: 1,
              }),
        unit: 'request',
      })
    }
    return entries
  }

  return BILLING_PRICING_VARS.flatMap((variable) => {
    if (!variable.field) return []
    const value = Number((tier as ParsedTier)[variable.field])
    if (!Number.isFinite(value) || value <= 0) return []

    return [
      {
        key: variable.key,
        field: variable.field,
        label: variable.label,
        shortLabel: variable.shortLabel,
        labelKind: 'i18n' as const,
        value,
        formatted: formatDynamicUnitPrice(value, options),
        unit: 'token' as const,
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
  const isTaskUsage = isTaskUsagePricingModel(model)
  const tiers: DynamicPricingTier[] = isTaskUsage
    ? parseTaskTiersFromExpr(billingExpr, model.billing_usage_schema)
    : parseTiersFromExpr(billingExpr)
  const tier = isTaskUsage
    ? (tiers.at(-1) ?? null)
    : getDisplayedTier(tiers as ParsedTier[], billingExpr, options.now)
  const entries = getDynamicPriceEntries(tier, {
    ...options,
    usageSchema: model.billing_usage_schema,
  })
  if (isTaskUsage && entries.length > 0) {
    for (const entry of entries) {
      const values = tiers
        .filter(isTaskPricingTier)
        .map((candidate) => {
          if (entry.field === 'constant') return candidate.constant
          return candidate.unitPrices[entry.field] ?? 0
        })
        .filter((value) => Number.isFinite(value) && value > 0)
      const minimum = Math.min(...values)
      const maximum = Math.max(...values)
      if (values.length > 1 && minimum !== maximum) {
        entry.formattedRange = `${formatTaskUsageUnitPrice(minimum, options)} – ${formatTaskUsageUnitPrice(maximum, options)}`
      }
    }
  }
  const rawExpression = model.billing_expr || ''

  return {
    tiers,
    tier,
    tierCount: tiers.length,
    hasRequestRules: hasDynamicRequestRules(model),
    isSpecialExpression: rawExpression.trim().length > 0 && tiers.length === 0,
    rawExpression,
    entries,
    primaryEntries: isTaskUsage
      ? entries.filter((entry) => entry.unit !== 'request')
      : entries.filter((entry) => PRIMARY_DYNAMIC_FIELDS.has(entry.field)),
    secondaryEntries: isTaskUsage
      ? entries.filter((entry) => entry.unit === 'request')
      : entries.filter((entry) => !PRIMARY_DYNAMIC_FIELDS.has(entry.field)),
    isTaskUsage,
  }
}

export function getCardExamplePrice(
  model: PricingModel,
  options: DynamicPriceOptions
): CardExamplePrice | null {
  if (!isTaskUsagePricingModel(model)) return null
  const schema = model.billing_usage_schema
  const firstExample = model.billing_usage_examples?.[0]
  if (!schema || !firstExample) return null

  const { billingExpr } = splitBillingExprAndRequestRules(
    model.billing_expr || ''
  )
  const config = tryParseTaskVisualConfig(billingExpr, schema)
  if (!config) return null

  const result = evaluateTaskVisualConfig(config, firstExample.facts, schema)
  if (!result) return null

  return {
    label: firstExample.label,
    formatted: formatTaskUsageUnitPrice(result.total, options),
  }
}
