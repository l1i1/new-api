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
import { RefreshCw } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatTimestampToDate } from '@/lib/format'

import { listFitCapabilityMarks } from '../api'
import {
  FIT_CAPABILITY_STATE_LABEL_KEYS,
  FIT_CAPABILITY_STATE_VARIANTS,
  fitCapabilitySourceLabelKey,
  truncateFitHash,
} from '../lib/policy-format'
import type { FitCapabilityMark, FitCapabilityQuery } from '../types'

const PAGE_SIZE = 20

type Filters = {
  channelId: string
  family: string
  behavior: string
  supported: '' | 'true' | 'false'
}

const EMPTY_FILTERS: Filters = {
  channelId: '',
  family: '',
  behavior: '',
  supported: '',
}

/**
 * Fleet-wide capability marks.
 *
 * Read-only on purpose: a mark is evidence recorded by the controlled suite
 * applier, and the endpoint that writes one compares-and-swaps a revision. An
 * administrator editing a measurement by hand from a table would defeat both
 * properties, so the page shows what is claimed rather than letting it be
 * asserted. What it must do is make drift visible — a mark measured against a
 * policy that no longer exists, or one whose channel is gone.
 */
export function CapabilityMarksPanel() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [draftFilters, setDraftFilters] = useState<Filters>(EMPTY_FILTERS)
  const [filters, setFilters] = useState<Filters>(EMPTY_FILTERS)

  const query: FitCapabilityQuery = {
    page,
    pageSize: PAGE_SIZE,
    channelId: filters.channelId || undefined,
    family: filters.family || undefined,
    behavior: filters.behavior || undefined,
    supported: filters.supported || undefined,
  }

  const marksQuery = useQuery({
    queryKey: ['fit-capability-marks', query],
    queryFn: () => listFitCapabilityMarks(query),
  })
  const marks = marksQuery.data?.items ?? []
  const pageCount = Math.max(
    1,
    Math.ceil((marksQuery.data?.total ?? 0) / PAGE_SIZE)
  )

  let content: ReactNode
  if (marksQuery.isError) {
    content = (
      <ErrorState
        title={t('Failed to load')}
        description={
          marksQuery.error instanceof Error
            ? marksQuery.error.message
            : undefined
        }
        onRetry={() => marksQuery.refetch()}
      />
    )
  } else if (marksQuery.isLoading) {
    content = <Skeleton className='h-64 w-full' />
  } else if (marks.length === 0) {
    content = (
      <EmptyState
        title={t('No capability marks match these filters.')}
        description={t(
          'A mark appears here once a suite run reports it or an operator records one.'
        )}
      />
    )
  } else {
    content = (
      <MarksTable
        marks={marks}
        total={marksQuery.data?.total ?? 0}
        page={page}
        pageCount={pageCount}
        onPageChange={setPage}
      />
    )
  }

  return (
    <Card>
      <CardHeader className='flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between'>
        <div className='space-y-1'>
          <CardTitle>{t('Capability marks')}</CardTitle>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Recording a mark is the job of the controlled suite applier: it needs the capability.write permission, which no role holds by default.'
            )}
          </p>
        </div>
        <Button
          variant='outline'
          size='sm'
          onClick={() => marksQuery.refetch()}
          disabled={marksQuery.isFetching}
        >
          <RefreshCw className={marksQuery.isFetching ? 'animate-spin' : ''} />
          {t('Refresh')}
        </Button>
      </CardHeader>
      <CardContent className='space-y-4'>
        <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-5'>
          <div className='space-y-1'>
            <Label htmlFor='fit-mark-channel'>{t('Channel ID')}</Label>
            <Input
              id='fit-mark-channel'
              inputMode='numeric'
              value={draftFilters.channelId}
              onChange={(event) =>
                setDraftFilters((current) => ({
                  ...current,
                  channelId: event.target.value,
                }))
              }
              placeholder='1'
            />
          </div>
          <div className='space-y-1'>
            <Label htmlFor='fit-mark-family'>{t('Family')}</Label>
            <Input
              id='fit-mark-family'
              value={draftFilters.family}
              onChange={(event) =>
                setDraftFilters((current) => ({
                  ...current,
                  family: event.target.value,
                }))
              }
              placeholder='kimi-k3'
            />
          </div>
          <div className='space-y-1'>
            <Label htmlFor='fit-mark-behavior'>{t('Behavior')}</Label>
            <Input
              id='fit-mark-behavior'
              value={draftFilters.behavior}
              onChange={(event) =>
                setDraftFilters((current) => ({
                  ...current,
                  behavior: event.target.value,
                }))
              }
              placeholder='tools.dynamic_names'
            />
          </div>
          <div className='space-y-1'>
            <Label htmlFor='fit-mark-supported'>{t('Supported')}</Label>
            <NativeSelect
              id='fit-mark-supported'
              value={draftFilters.supported}
              onChange={(event) =>
                setDraftFilters((current) => ({
                  ...current,
                  supported: event.target.value as Filters['supported'],
                }))
              }
            >
              <NativeSelectOption value=''>{t('All')}</NativeSelectOption>
              <NativeSelectOption value='true'>
                {t('Enabled')}
              </NativeSelectOption>
              <NativeSelectOption value='false'>
                {t('Unsupported only')}
              </NativeSelectOption>
            </NativeSelect>
          </div>
          <div className='flex items-end gap-2'>
            <Button
              size='sm'
              onClick={() => {
                setPage(1)
                setFilters(draftFilters)
              }}
            >
              {t('Apply')}
            </Button>
            <Button
              size='sm'
              variant='ghost'
              onClick={() => {
                setDraftFilters(EMPTY_FILTERS)
                setFilters(EMPTY_FILTERS)
                setPage(1)
              }}
            >
              {t('Reset')}
            </Button>
          </div>
        </div>
        {content}
      </CardContent>
    </Card>
  )
}

function MarksTable(props: {
  marks: FitCapabilityMark[]
  total: number
  page: number
  pageCount: number
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  return (
    <>
      <div className='overflow-x-auto'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('Channel')}</TableHead>
              <TableHead>{t('Family / model')}</TableHead>
              <TableHead>{t('Behavior')}</TableHead>
              <TableHead>{t('Supported')}</TableHead>
              <TableHead>{t('Source')}</TableHead>
              <TableHead>{t('State')}</TableHead>
              <TableHead>{t('Binding')}</TableHead>
              <TableHead>{t('Expires at')}</TableHead>
              <TableHead>{t('Last verified')}</TableHead>
              <TableHead>{t('Revision')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {props.marks.map((mark) => (
              <TableRow key={mark.id}>
                <TableCell>
                  <div className='font-medium'>
                    {mark.channel_name || `#${mark.channel_id}`}
                  </div>
                  <div className='text-muted-foreground text-xs'>
                    #{mark.channel_id}
                  </div>
                </TableCell>
                <TableCell>
                  <div>{mark.family}</div>
                  <div className='text-muted-foreground text-xs'>
                    {mark.model}
                  </div>
                </TableCell>
                <TableCell className='font-mono text-xs'>
                  {mark.behavior}
                </TableCell>
                <TableCell>
                  <Badge variant={mark.supported ? 'default' : 'destructive'}>
                    {mark.supported ? t('Supported') : t('Unsupported')}
                  </Badge>
                </TableCell>
                <TableCell className='text-xs'>
                  {t(fitCapabilitySourceLabelKey(mark.source))}
                </TableCell>
                <TableCell>
                  <Badge variant={FIT_CAPABILITY_STATE_VARIANTS[mark.state]}>
                    {t(FIT_CAPABILITY_STATE_LABEL_KEYS[mark.state])}
                  </Badge>
                </TableCell>
                <TableCell>
                  <BindingCell mark={mark} />
                </TableCell>
                <TableCell className='text-xs'>
                  {formatTimestampToDate(mark.expires_at)}
                </TableCell>
                <TableCell className='text-xs'>
                  {formatTimestampToDate(mark.at)}
                </TableCell>
                <TableCell className='text-xs'>{mark.revision}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <div className='flex items-center justify-between'>
        <p className='text-muted-foreground text-sm'>
          {t('Total')}: {props.total}
        </p>
        <div className='flex items-center gap-2'>
          <Button
            variant='outline'
            size='sm'
            disabled={props.page <= 1}
            onClick={() => props.onPageChange(Math.max(1, props.page - 1))}
          >
            {t('Previous')}
          </Button>
          <span className='text-sm'>
            {props.page} / {props.pageCount}
          </span>
          <Button
            variant='outline'
            size='sm'
            disabled={props.page >= props.pageCount}
            onClick={() => props.onPageChange(props.page + 1)}
          >
            {t('Next')}
          </Button>
        </div>
      </div>
    </>
  )
}

/**
 * The provenance hashes. They are shown because the whole point of the binding
 * is that a mark measured against a replaced policy is stale rather than
 * silently still valid — and that is only checkable if both values are visible.
 */
function BindingCell({ mark }: { mark: FitCapabilityMark }) {
  const { t } = useTranslation()
  const policy = truncateFitHash(mark.policy_hash)
  const baseline = truncateFitHash(mark.baseline_hash)
  return (
    <div className='space-y-1'>
      <div className='font-mono text-xs'>
        <span className='text-muted-foreground'>p </span>
        {policy || '-'}
      </div>
      <div className='font-mono text-xs'>
        <span className='text-muted-foreground'>b </span>
        {baseline || '-'}
      </div>
      <Badge variant={mark.binding_current ? 'outline' : 'warning'}>
        {mark.binding_current
          ? t('Bound to the live policy')
          : t('Measured against a superseded policy')}
      </Badge>
    </div>
  )
}
