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

import { IS_MAINLAND_SITE } from '@/lib/site-flavor'

export type PricingCurrencyPreference = 'USD' | 'site'

type PricingPreferences = {
  currency: PricingCurrencyPreference
  setCurrency: (currency: PricingCurrencyPreference) => void
}

export const usePricingPreferencesStore = create<PricingPreferences>()(
  persist(
    (set) => ({
      // The mainland edition prices everything in the platform's own unit, so
      // the site currency is the only valid preference there.
      currency: IS_MAINLAND_SITE ? 'site' : 'USD',
      setCurrency: (currency) =>
        set({ currency: IS_MAINLAND_SITE ? 'site' : currency }),
    }),
    {
      name: 'model-pricing-preferences',
      partialize: (state) => ({ currency: state.currency }),
      // A stored 'USD' preference from before the mainland edition pinned the
      // site currency must not rehydrate.
      merge: (persisted, current) =>
        IS_MAINLAND_SITE
          ? current
          : { ...current, ...(persisted as Partial<PricingPreferences>) },
    }
  )
)
