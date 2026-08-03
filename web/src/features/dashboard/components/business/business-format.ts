import type { BusinessAnalysisReport } from '@/features/dashboard/types'
import type { DisplayCurrency } from '@/stores/currency-display-store'

function formatAmount(value: number, currency: DisplayCurrency): string {
  const symbol = currency === 'CNY' ? '¥' : '$'
  return `${symbol}${value.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`
}

export function formatNumber(value: number): string {
  return Math.round(value).toLocaleString()
}

export function formatCNY(
  value: number,
  report: BusinessAnalysisReport,
  currency: DisplayCurrency
): string {
  const usd = report.cny_per_usd > 0 ? value / report.cny_per_usd : 0
  return formatAmount(currency === 'CNY' ? value : usd, currency)
}

export function formatQuotaMoney(
  quota: number,
  report: BusinessAnalysisReport,
  currency: DisplayCurrency
): string {
  const usd = report.quota_per_unit > 0 ? quota / report.quota_per_unit : 0
  return formatAmount(
    currency === 'CNY' ? usd * report.cny_per_usd : usd,
    currency
  )
}

export function formatPercent(value: number): string {
  return `${(value * 100).toLocaleString(undefined, {
    maximumFractionDigits: 1,
  })}%`
}
