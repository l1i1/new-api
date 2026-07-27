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
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTablePage, useDataTable } from '@/components/data-table'

import { getAdminPlans } from '../api'
import { useSubscriptionsColumns } from './subscriptions-columns'
import { useSubscriptions } from './subscriptions-provider'

export function SubscriptionsTable() {
  const { i18n, t } = useTranslation()
  const columns = useSubscriptionsColumns()
  const { refreshTrigger } = useSubscriptions()
  const contentLanguage = i18n.resolvedLanguage || i18n.language

  const { data, isLoading } = useQuery({
    queryKey: ['admin-subscription-plans', refreshTrigger],
    queryFn: async () => {
      const result = await getAdminPlans()
      return result.data || []
    },
    placeholderData: (prev) => prev,
  })

  // TanStack caches localized accessor values on each row. Rebuild the row
  // model when the active content language changes, preserving raw plan data.
  const plans = useMemo(
    () => (contentLanguage && data ? [...data] : []),
    [contentLanguage, data]
  )

  const { table } = useDataTable({
    data: plans,
    columns,
    withFilteredRowModel: false,
    withFacetedRowModel: false,
  })

  return (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={isLoading}
      emptyTitle={t('No subscription plans yet')}
      emptyDescription={t(
        'Click "Create Plan" to create your first subscription plan'
      )}
      skeletonKeyPrefix='subscriptions-skeleton'
      applyHeaderSize
    />
  )
}
