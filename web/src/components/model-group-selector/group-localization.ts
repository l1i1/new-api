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

export interface GroupOption {
  label: string
  value: string
  ratio?: number
  desc?: string
  description?: string
}

export function localizeModelGroupDescriptions(
  groups: GroupOption[],
  language?: string
): GroupOption[] {
  return groups.map((group) => {
    const desc =
      group.desc === undefined
        ? undefined
        : resolveTntContent(group.desc, language)
    const description =
      group.description === undefined
        ? undefined
        : resolveTntContent(group.description, language)

    if (desc === group.desc && description === group.description) {
      return group
    }

    return { ...group, desc, description }
  })
}

export function modelGroupMatchesSearch(
  group: GroupOption,
  search: string
): boolean {
  const searchTerm = search.trim().toLowerCase()
  if (!searchTerm) {
    return true
  }

  const searchableFields = [
    group.label,
    group.desc || '',
    group.description || '',
    group.value,
  ]
    .join(' ')
    .toLowerCase()

  return searchableFields.includes(searchTerm)
}

export function filterModelGroupOptions(
  groups: GroupOption[],
  search: string
): GroupOption[] {
  return groups.filter((group) => modelGroupMatchesSearch(group, search))
}
