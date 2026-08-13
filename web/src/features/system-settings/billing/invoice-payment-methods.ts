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
import type { Option as MultiSelectOption } from '@/components/multi-select'

export function normalizePaymentMethodValues(values: string[]): string[] {
  return [
    ...new Set(
      values
        .filter((value): value is string => typeof value === 'string')
        .map((value) => value.trim().toLowerCase())
        .filter(Boolean)
    ),
  ].sort()
}

export function getPaymentMethodOptions(
  paymentMethodConfig: string,
  selectedValues: string[],
  t: (key: string) => string
): MultiSelectOption[] {
  const labels: Record<string, string> = {
    alipay: t('Alipay'),
    wxpay: t('WeChat Pay'),
    stripe: 'Stripe',
    creem: 'Creem',
    waffo: 'Waffo',
    waffo_pancake: 'Waffo Pancake',
  }
  const values = new Set(Object.keys(labels))
  try {
    const parsed: unknown = JSON.parse(paymentMethodConfig || '[]')
    if (Array.isArray(parsed)) {
      for (const item of parsed) {
        if (typeof item === 'object' && item !== null && 'type' in item) {
          const type = (item as { type?: unknown }).type
          if (typeof type === 'string' && type.trim()) {
            values.add(type.trim().toLowerCase())
          }
        }
      }
    }
  } catch {
    // The server validates the payment method JSON separately.
  }
  for (const value of selectedValues) values.add(value)
  return [...values]
    .sort()
    .map((value) => ({ label: labels[value] ?? value, value }))
}
