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
import { describe, expect, it, vi } from 'vitest'

localStorage.clear()

vi.mock('@/lib/site-flavor', () => ({
  IS_MAINLAND_SITE: false,
  getSiteFlavor: () => 'overseas',
}))

describe('i18n overseas flavour', () => {
  it('keeps the detected locale and the original USD wording', async () => {
    const { default: i18n } = await import('../config')
    if (!i18n.isInitialized) {
      await new Promise<void>((resolve) => {
        i18n.on('initialized', () => resolve())
      })
    }

    expect(i18n.resolvedLanguage).toBe('en')
    expect(i18n.t('Recharge Amount (USD)')).toBe('Recharge Amount (USD)')
    expect(i18n.t('CNY per USD')).toBe('CNY per USD')

    await i18n.changeLanguage('zhCN')
    expect(i18n.resolvedLanguage).toBe('zhCN')
    expect(i18n.t('Recharge Amount (USD)')).toBe('充值金额 (USD)')
  })
})