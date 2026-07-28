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
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import {
  Building2,
  ChevronLeft,
  ChevronRight,
  Loader2,
  Pencil,
  Plus,
  RefreshCcw,
} from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

import { getVendors } from '../../api'
import { vendorsQueryKeys } from '../../lib'
import type { Vendor } from '../../types'

type VendorManagementDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreateVendor: () => void
  onEditVendor: (vendor: Vendor) => void
}

const VENDORS_PAGE_SIZE = 100

export function VendorManagementDialog({
  open,
  onOpenChange,
  onCreateVendor,
  onEditVendor,
}: VendorManagementDialogProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const { data, error, isFetching, isLoading, refetch } = useQuery({
    queryKey: vendorsQueryKeys.list({ p: page, page_size: VENDORS_PAGE_SIZE }),
    queryFn: () => getVendors({ p: page, page_size: VENDORS_PAGE_SIZE }),
    placeholderData: keepPreviousData,
    enabled: open,
  })

  const vendors = useMemo(
    () =>
      [...(data?.data?.items ?? [])].sort((a, b) =>
        a.name.localeCompare(b.name)
      ),
    [data?.data?.items]
  )
  const total = data?.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / VENDORS_PAGE_SIZE))

  useEffect(() => {
    if (!open) {
      setPage(1)
    }
  }, [open])

  useEffect(() => {
    if (page > totalPages) {
      setPage(totalPages)
    }
  }, [page, totalPages])

  let vendorsContent: ReactNode
  if (error) {
    vendorsContent = (
      <p role='alert' className='text-destructive py-8 text-center text-sm'>
        {(error as Error).message || t('Failed to load')}
      </p>
    )
  } else if (isLoading) {
    vendorsContent = (
      <div className='text-muted-foreground flex items-center justify-center gap-2 py-12 text-sm'>
        <Loader2 className='h-5 w-5 animate-spin' />
        {t('Loading...')}
      </div>
    )
  } else {
    vendorsContent = (
      <StaticDataTable
        tableClassName='min-w-[640px]'
        data={vendors}
        getRowKey={(vendor) => vendor.id}
        emptyContent={t('No data')}
        columns={[
          {
            id: 'name',
            header: t('Name'),
            cellClassName: 'font-medium',
            cell: (vendor) => vendor.name,
          },
          {
            id: 'display-name',
            header: t('Display Name'),
            className: 'min-w-[220px]',
            cellClassName: 'text-muted-foreground whitespace-normal',
            cell: (vendor) => vendor.display_name || '-',
          },
          {
            id: 'description',
            header: t('Description'),
            className: 'min-w-[180px]',
            cellClassName: 'text-muted-foreground whitespace-normal',
            cell: (vendor) => vendor.description || '-',
          },
          {
            id: 'actions',
            header: t('Actions'),
            className: 'w-16 text-right',
            cellClassName: 'text-right',
            cell: (vendor) => (
              <Button
                variant='ghost'
                size='icon-sm'
                onClick={() => onEditVendor(vendor)}
                aria-label={`${t('Edit Vendor')}: ${vendor.name}`}
              >
                <Pencil className='h-4 w-4' />
              </Button>
            ),
          },
        ]}
      />
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        <>
          <Building2 className='text-foreground/80 h-5 w-5' />
          {t('Manage Vendors')}
        </>
      }
      description={t('Add a new vendor to the system')}
      contentClassName='w-[calc(100vw-2rem)] sm:max-w-[52rem]'
      titleClassName='flex items-center gap-2 text-lg'
      contentHeight='auto'
      bodyClassName='space-y-3'
    >
      <div className='bg-muted/30 flex flex-wrap items-center justify-between gap-3 rounded-md border p-2'>
        <Button size='sm' onClick={onCreateVendor}>
          <Plus className='h-4 w-4' />
          {t('Create Vendor')}
        </Button>
        <Button
          size='sm'
          variant='ghost'
          onClick={() => refetch()}
          disabled={isFetching}
        >
          {isFetching ? (
            <Loader2 className='h-4 w-4 animate-spin' />
          ) : (
            <RefreshCcw className='h-4 w-4' />
          )}
          {t('Refresh')}
        </Button>
      </div>

      {vendorsContent}

      {totalPages > 1 && (
        <div className='flex items-center justify-between gap-3'>
          <p className='text-muted-foreground text-sm'>
            {t('Page {{current}} of {{total}}', {
              current: page,
              total: totalPages,
            })}
          </p>
          <div className='flex items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              size='icon-sm'
              onClick={() => setPage((current) => Math.max(1, current - 1))}
              disabled={page === 1 || isFetching}
              aria-label={t('Previous')}
            >
              <ChevronLeft className='h-4 w-4' />
            </Button>
            <Button
              type='button'
              variant='outline'
              size='icon-sm'
              onClick={() =>
                setPage((current) => Math.min(totalPages, current + 1))
              }
              disabled={page === totalPages || isFetching}
              aria-label={t('Next')}
            >
              <ChevronRight className='h-4 w-4' />
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  )
}
