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
import type { ColumnDef, ColumnFiltersState, PaginationState } from '@tanstack/react-table'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { DataTablePage, useDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Button } from '@/components/ui/button'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { getAdminInvoices } from '../api'
import { getInvoiceStatusConfig, INVOICE_STATUS_OPTIONS } from '../lib/status'
import type { AdminInvoiceListItem } from '../types'

interface AdminInvoicesTableProps {
  refreshTrigger: number
  onViewDetail: (invoice: AdminInvoiceListItem) => void
}

export function AdminInvoicesTable({ refreshTrigger, onViewDetail }: AdminInvoicesTableProps) {
  const { t } = useTranslation()
  const [globalFilter, setGlobalFilter] = useState('')
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])
  const [pagination, setPagination] = useState<PaginationState>({ pageIndex: 0, pageSize: 20 })
  const activeStatus = ((columnFilters.find((f) => f.id === 'status')?.value as string[] | undefined) ?? [])[0] ?? ''

  const ensurePageInRange = useCallback((pageCount: number) => {
    setPagination((prev) => (pageCount > 0 && prev.pageIndex >= pageCount ? { ...prev, pageIndex: pageCount - 1 } : prev))
  }, [])

  const { data, isLoading, isFetching } = useQuery({
    queryKey: ['admin-invoices', pagination.pageIndex + 1, pagination.pageSize, globalFilter, activeStatus, refreshTrigger],
    queryFn: async () => {
      const result = await getAdminInvoices({
        page: pagination.pageIndex + 1,
        page_size: pagination.pageSize,
        keyword: globalFilter.trim() || undefined,
        status: activeStatus,
      })
      if (!result.success) {
        toast.error(result.message || t('Failed to load invoices'))
        return { items: [], total: 0 }
      }
      return { items: result.data?.items ?? [], total: result.data?.total ?? 0 }
    },
    placeholderData: (previousData) => previousData,
  })

  const columns: ColumnDef<AdminInvoiceListItem>[] = [
    {
      accessorKey: 'id',
      header: t('ID'),
      meta: { mobileHidden: true },
      cell: ({ row }) => <TableId value={row.original.id} />,
    },
    {
      accessorKey: 'user_id',
      header: t('User ID'),
      meta: { mobileHidden: true },
      cell: ({ row }) => <TableId value={row.original.user_id} />,
    },
    {
      accessorKey: 'title',
      header: t('Title'),
      meta: { mobileTitle: true },
      cell: ({ row }) => <span className='block max-w-[220px] truncate font-medium'>{row.original.title || '-'}</span>,
    },
    {
      id: 'total_amount',
      header: t('Amount'),
      meta: { mobileHidden: true },
      cell: ({ row }) => <span className='font-semibold tabular-nums'>{formatNumber(row.original.total_amount)} {row.original.currency}</span>,
    },
    {
      accessorKey: 'status',
      header: t('Status'),
      meta: { mobileBadge: true },
      cell: ({ row }) => {
        const config = getInvoiceStatusConfig(row.original.status)
        return <StatusBadge label={t(config.labelKey)} variant={config.variant} copyable={false} />
      },
    },
    {
      accessorKey: 'create_time',
      header: t('Created At'),
      meta: { mobileHidden: true },
      cell: ({ row }) => <span className='text-muted-foreground whitespace-nowrap tabular-nums'>{formatTimestampToDate(row.original.create_time)}</span>,
    },
    {
      id: 'actions',
      header: t('Actions'),
      meta: { pinned: 'right' as const },
      cell: ({ row }) => <Button variant='outline' size='sm' onClick={() => onViewDetail(row.original)}>{t('View Details')}</Button>,
    },
  ]

  const { table } = useDataTable({
    data: data?.items ?? [],
    columns,
    globalFilter,
    onGlobalFilterChange: setGlobalFilter,
    columnFilters,
    onColumnFiltersChange: setColumnFilters,
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    manualFiltering: true,
    totalCount: data?.total ?? 0,
    ensurePageInRange,
  })

  return (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={isLoading}
      isFetching={isFetching}
      emptyTitle={t('No invoices found')}
      emptyDescription={t('No invoices match the current search or filters')}
      skeletonKeyPrefix='admin-invoices-skeleton'
      applyHeaderSize
      toolbarProps={{
        searchPlaceholder: t('Search by title, tax ID, email or reason...'),
        searchDebounceMs: 500,
        filters: [
          {
            columnId: 'status',
            title: t('Status'),
            options: INVOICE_STATUS_OPTIONS.map((status) => ({
              value: status,
              label: t(getInvoiceStatusConfig(status).labelKey),
            })),
            singleSelect: true,
          },
        ],
      }}
    />
  )
}