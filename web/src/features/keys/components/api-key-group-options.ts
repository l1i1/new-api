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
import { resolveTntContent } from '@/lib/tnt-content'

export type ApiKeyGroupOption = {
  value: string
  label: string
  desc?: string
  ratio?: number | string
}

export function localizeApiKeyGroupDescriptions(
  options: ApiKeyGroupOption[],
  language?: string
): ApiKeyGroupOption[] {
  return options.map((option) => {
    if (option.desc === undefined) {
      return option
    }

    const desc = resolveTntContent(option.desc, language)
    return desc === option.desc ? option : { ...option, desc }
  })
}

export function filterApiKeyGroupOptions(
  options: ApiKeyGroupOption[],
  searchValue: string
): ApiKeyGroupOption[] {
  const search = searchValue.trim().toLowerCase()
  if (!search) {
    return options
  }

  return options.filter((option) => {
    const ratioText = String(option.ratio ?? '').toLowerCase()
    return (
      option.value.toLowerCase().includes(search) ||
      option.label.toLowerCase().includes(search) ||
      option.desc?.toLowerCase().includes(search) ||
      ratioText.includes(search)
    )
  })
}
