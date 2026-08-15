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
import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export type DisplayCurrency = 'CNY' | 'USD'

export function getDefaultDisplayCurrency(language?: string): DisplayCurrency {
  const browserLanguage =
    language ??
    (typeof navigator === 'undefined' ? undefined : navigator.language)

  return browserLanguage?.toLowerCase().startsWith('zh') ? 'CNY' : 'USD'
}

interface CurrencyDisplayState {
  currency: DisplayCurrency
  setCurrency: (currency: DisplayCurrency) => void
}

/**
 * User-local currency preference for display conversions and the Waffo Pancake
 * wallet checkout currency.
 *
 * Only Waffo Pancake wallet checkout consumes this value as a provider
 * currency; other payment channels keep their own immutable currency rules.
 * Account balances and server-side billing remain server-owned.
 */
export const useCurrencyDisplayStore = create<CurrencyDisplayState>()(
  persist(
    (set) => ({
      currency: getDefaultDisplayCurrency(),
      setCurrency: (currency) => set({ currency }),
    }),
    { name: 'currency-display-storage' }
  )
)
