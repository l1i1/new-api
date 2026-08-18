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
import { SearchIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { useSearch } from '@/context/search-provider'
import { cn } from '@/lib/utils'

import { Button } from './ui/button'

type SearchProps = {
  className?: string
  type?: React.HTMLInputTypeAttribute
  placeholder?: string
}

type SearchButtonProps = {
  className?: string
  label: string
  onOpen: () => void
}

export function SearchButton(props: SearchButtonProps) {
  return (
    <Button
      variant='ghost'
      size='icon'
      className={cn(
        'text-muted-foreground hover:bg-accent hover:text-foreground size-9 shrink-0 rounded-md shadow-none',
        props.className
      )}
      onClick={props.onOpen}
      aria-label={props.label}
      title={props.label}
    >
      <SearchIcon aria-hidden='true' className='size-4' />
    </Button>
  )
}

export function Search({ className = '', placeholder }: SearchProps) {
  const { t } = useTranslation()
  const { setOpen } = useSearch()
  const resolvedPlaceholder = placeholder ?? t('Search')

  return (
    <SearchButton
      className={className}
      label={resolvedPlaceholder}
      onOpen={() => setOpen(true)}
    />
  )
}
