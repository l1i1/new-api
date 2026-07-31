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
*/
import { Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import { useCurrencyDisplayStore } from '@/stores/currency-display-store'

const displayCurrencyOptions = [
  { value: 'CNY', label: '¥ CNY' },
  { value: 'USD', label: '$ USD' },
] as const

export function CurrencyDisplaySwitcher() {
  const { t } = useTranslation()
  const currency = useCurrencyDisplayStore((state) => state.currency)
  const setCurrency = useCurrencyDisplayStore((state) => state.setCurrency)

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger
        render={
          <Button
            variant='ghost'
            size='sm'
            className='h-9 min-w-9 px-2 text-sm font-semibold tabular-nums'
            aria-label={t('Currency')}
            title={t('Currency')}
          />
        }
      >
        {currency === 'CNY' ? '¥' : '$'}
      </DropdownMenuTrigger>
      <DropdownMenuContent align='end'>
        {displayCurrencyOptions.map((option) => (
          <DropdownMenuItem
            key={option.value}
            onClick={() => setCurrency(option.value)}
          >
            {option.label}
            <Check
              size={14}
              className={cn('ms-auto', currency !== option.value && 'hidden')}
            />
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
