import type { BusinessAnalysisReport } from '@/features/dashboard/types'

export function formatQuota(value: number): string {
  return Math.round(value).toLocaleString()
}

export function formatCNY(value: number): string {
  return `¥${value.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`
}

export function formatQuotaMoney(
  quota: number,
  report: BusinessAnalysisReport
): string {
  const usd = report.quota_per_unit > 0 ? quota / report.quota_per_unit : 0
  return `${formatCNY(usd * report.cny_per_usd)} · $${usd.toLocaleString(
    undefined,
    {
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    }
  )}`
}

export function formatPercent(value: number): string {
  return `${(value * 100).toLocaleString(undefined, {
    maximumFractionDigits: 1,
  })}%`
}
