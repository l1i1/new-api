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
import { useQueryClient } from '@tanstack/react-query'
import {
  FlaskConical,
  Loader2,
  Power,
  PowerOff,
  RefreshCw,
  Square,
  Trash2,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  getMultiKeyStatus,
  enableMultiKey,
  disableMultiKey,
  deleteMultiKey,
  enableAllMultiKeys,
  disableAllMultiKeys,
  deleteDisabledMultiKeys,
  testMultiKeys,
  getMultiKeyTestTask,
  updateMultiKeyStatus,
  updateMultiKeyProxy,
  getAllChannelObservability,
  cancelMultiKeyTestTask,
} from '../../api'
import { MULTI_KEY_FILTER_OPTIONS } from '../../constants'
import {
  channelsQueryKeys,
  aggregateMultiKeyObservability,
  formatTimestamp,
  formatMultiKeyTestResult,
  getMultiKeyIndex,
  getMultiKeyTestResult,
  getMultiKeyStatusConfig,
  getMultiKeyConfirmMessage,
  isDestructiveAction,
} from '../../lib'
import type {
  ChannelObservabilityResult,
  KeyStatus,
  MultiKeyConfirmAction,
  MultiKeyTestResult,
} from '../../types'
import { useChannels } from '../channels-provider'
import { StatisticsCard } from './multi-key-statistics-card'
import { MultiKeyTableRowActions } from './multi-key-table-row-actions'

type MultiKeyManageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function MultiKeyManageDialog({
  open,
  onOpenChange,
}: MultiKeyManageDialogProps) {
  const { t } = useTranslation()
  const { currentRow } = useChannels()
  const queryClient = useQueryClient()
  const currentUser = useAuthStore((s) => s.auth.user)
  const canEditSensitive = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )

  // Data state
  const [isLoading, setIsLoading] = useState(false)
  const [keys, setKeys] = useState<KeyStatus[]>([])
  const [currentPage, setCurrentPage] = useState(1)
  const [pageSize, setPageSize] = useState(10)
  const [total, setTotal] = useState(0)
  const [totalPages, setTotalPages] = useState(0)
  const [enabledCount, setEnabledCount] = useState(0)
  const [manualDisabledCount, setManualDisabledCount] = useState(0)
  const [autoDisabledCount, setAutoDisabledCount] = useState(0)
  const [keysRevision, setKeysRevision] = useState<number | undefined>()

  // UI state
  const [statusFilter, setStatusFilter] = useState<number | null>(null)
  const [confirmAction, setConfirmAction] =
    useState<MultiKeyConfirmAction | null>(null)
  const [isPerformingAction, setIsPerformingAction] = useState(false)
  const [isTestingKeys, setIsTestingKeys] = useState(false)
  const [testTaskId, setTestTaskId] = useState<string | null>(null)
  const [testResults, setTestResults] = useState<
    Record<number, MultiKeyTestResult>
  >({})
  const [failedCredentialIds, setFailedCredentialIds] = useState<number[]>([])
  const [failedOnly, setFailedOnly] = useState(false)
  const [keyMetrics, setKeyMetrics] = useState<
    Record<number, ChannelObservabilityResult>
  >({})
  const [proxyTarget, setProxyTarget] = useState<KeyStatus | null>(null)
  const [proxyMode, setProxyMode] = useState<'inherit' | 'direct' | 'custom'>(
    'inherit'
  )
  const [proxyUrl, setProxyUrl] = useState('')
  const [isSavingProxy, setIsSavingProxy] = useState(false)
  const [selectedCredentialIds, setSelectedCredentialIds] = useState<number[]>(
    []
  )
  const [proxyBatch, setProxyBatch] = useState(false)
  const activeChannelId = useRef<number | null>(null)
  const loadSequence = useRef(0)

  const resetChannelState = () => {
    setIsLoading(false)
    setKeys([])
    setCurrentPage(1)
    setPageSize(10)
    setTotal(0)
    setTotalPages(0)
    setEnabledCount(0)
    setManualDisabledCount(0)
    setAutoDisabledCount(0)
    setKeysRevision(undefined)
    setStatusFilter(null)
    setConfirmAction(null)
    setIsPerformingAction(false)
    setIsTestingKeys(false)
    setTestTaskId(null)
    setTestResults({})
    setFailedCredentialIds([])
    setFailedOnly(false)
    setKeyMetrics({})
    setProxyTarget(null)
    setProxyMode('inherit')
    setProxyUrl('')
    setIsSavingProxy(false)
    setSelectedCredentialIds([])
    setProxyBatch(false)
  }

  const isCurrentChannel = (channelId: number) =>
    activeChannelId.current === channelId

  // Reset and load data when dialog opens
  useEffect(() => {
    if (open && currentRow) {
      activeChannelId.current = currentRow.id
      loadSequence.current += 1
      resetChannelState()
      void loadKeyStatus(1, 10, null)
      return
    }
    activeChannelId.current = null
    loadSequence.current += 1
    resetChannelState()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, currentRow?.id])

  const loadKeyStatus = async (
    page: number = currentPage,
    size: number = pageSize,
    status: number | null = statusFilter
  ) => {
    const channelId = currentRow?.id
    if (!channelId) return
    const requestSequence = ++loadSequence.current
    const isCurrentLoad = () =>
      isCurrentChannel(channelId) && loadSequence.current === requestSequence

    setIsLoading(true)
    try {
      const response = await getMultiKeyStatus(
        channelId,
        page,
        size,
        status === null ? undefined : status
      )

      if (!isCurrentLoad()) return
      if (response.success && response.data) {
        setKeys(response.data.keys || [])
        setTotal(response.data.total || 0)
        setCurrentPage(response.data.page || 1)
        setPageSize(response.data.page_size || 10)
        setTotalPages(response.data.total_pages || 0)
        setEnabledCount(response.data.enabled_count || 0)
        setManualDisabledCount(response.data.manual_disabled_count || 0)
        setAutoDisabledCount(response.data.auto_disabled_count || 0)
        setKeysRevision(response.data.keys_revision)
        try {
          const metricsItems = await getAllChannelObservability(channelId, 24)
          if (!isCurrentLoad()) return
          setKeyMetrics(aggregateMultiKeyObservability(metricsItems))
        } catch {
          if (!isCurrentLoad()) return
          setKeyMetrics({})
        }
      } else {
        toast.error(response.message || t('Failed to load key status'))
      }
    } catch (error: unknown) {
      if (!isCurrentLoad()) return
      toast.error(
        error instanceof Error ? error.message : t('Failed to load key status')
      )
    } finally {
      if (isCurrentLoad()) setIsLoading(false)
    }
  }

  const handleStatusFilterChange = (value: string) => {
    const newFilter = value === 'all' ? null : Number.parseInt(value)
    setStatusFilter(newFilter)
    setCurrentPage(1)
    loadKeyStatus(1, pageSize, newFilter)
  }

  const handlePageChange = (newPage: number) => {
    setCurrentPage(newPage)
    loadKeyStatus(newPage, pageSize)
  }

  const performAction = async () => {
    if (!confirmAction || !currentRow) return
    const channelId = currentRow.id
    if (!isCurrentChannel(channelId)) return
    if (
      !canEditSensitive &&
      (confirmAction.type === 'delete' ||
        confirmAction.type === 'delete-disabled')
    ) {
      setConfirmAction(null)
      return
    }

    setIsPerformingAction(true)
    try {
      const { type, keyIndex } = confirmAction
      let response

      // Execute the appropriate action
      if (type === 'enable-selected' || type === 'disable-selected') {
        response = await updateMultiKeyStatus(channelId, {
          credential_ids: confirmAction.credentialIds,
          status: type === 'enable-selected' ? 'enabled' : 'manual_disabled',
          keys_revision: keysRevision,
        })
      } else if (type === 'enable' && keyIndex !== undefined) {
        response = await enableMultiKey(channelId, keyIndex)
      } else if (type === 'disable' && keyIndex !== undefined) {
        response = await disableMultiKey(channelId, keyIndex)
      } else if (type === 'delete' && keyIndex !== undefined) {
        response = await deleteMultiKey(channelId, keyIndex)
      } else if (type === 'enable-all') {
        response = await enableAllMultiKeys(channelId)
      } else if (type === 'disable-all') {
        response = await disableAllMultiKeys(channelId)
      } else if (type === 'delete-disabled') {
        response = await deleteDisabledMultiKeys(channelId)
      }

      if (!isCurrentChannel(channelId)) return
      if (response?.success) {
        toast.success(response.message || t('Operation successful'))
        queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })

        // Reload data - reset to page 1 for bulk actions
        const isBulkAction =
          type.includes('all') ||
          type.includes('selected') ||
          type === 'delete-disabled'
        if (isBulkAction) {
          setCurrentPage(1)
          loadKeyStatus(1, pageSize)
        } else {
          loadKeyStatus(currentPage, pageSize)
        }
      } else {
        toast.error(response?.message || t('Operation failed'))
      }
    } catch (error: unknown) {
      if (!isCurrentChannel(channelId)) return
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    } finally {
      if (isCurrentChannel(channelId)) {
        setIsPerformingAction(false)
        setConfirmAction(null)
      }
    }
  }

  const runKeyTest = async (all: boolean) => {
    if (!currentRow || isTestingKeys) return
    const channelId = currentRow.id
    if (!isCurrentChannel(channelId)) return
    setIsTestingKeys(true)
    try {
      let selectedIds: number[] | undefined
      if (!all) {
        selectedIds =
          selectedCredentialIds.length > 0
            ? selectedCredentialIds
            : keys
                .filter((key) => key.status === 1 && key.credential_id)
                .map((key) => key.credential_id as number)
      }
      if (!all && (!selectedIds || selectedIds.length === 0)) {
        toast.error(t('Select at least one key'))
        return
      }
      const response = await testMultiKeys(channelId, {
        all,
        include_disabled: all,
        credential_ids: all ? undefined : selectedIds,
        concurrency: 4,
        timeout: 60,
      })
      if (!response.success) {
        if (!isCurrentChannel(channelId)) return
        toast.error(response.message || t('Operation failed'))
        return
      }
      const taskId = response.data?.task_id
      if (!taskId) {
        if (!isCurrentChannel(channelId)) return
        toast.error(t('Operation failed'))
        return
      }
      if (!isCurrentChannel(channelId)) return
      setTestTaskId(taskId)
      let taskResponse = await getMultiKeyTestTask(channelId, taskId)
      const deadline = Date.now() + 120_000
      while (
        taskResponse.data &&
        (taskResponse.data.status === 'pending' ||
          taskResponse.data.status === 'running') &&
        Date.now() < deadline
      ) {
        await new Promise((resolve) => window.setTimeout(resolve, 500))
        if (!isCurrentChannel(channelId)) return
        taskResponse = await getMultiKeyTestTask(channelId, taskId)
      }
      if (!isCurrentChannel(channelId)) return
      const results = taskResponse.data?.result?.results || []
      const nextResults: Record<number, MultiKeyTestResult> = {}
      const nextFailedCredentialIds: number[] = []
      for (const result of results) {
        const resultKey =
          result.credential_id > 0 ? result.credential_id : result.index
        nextResults[resultKey] = result
        if (result.status === 'failed' && result.credential_id > 0) {
          nextFailedCredentialIds.push(result.credential_id)
        }
      }
      setTestResults(nextResults)
      setFailedCredentialIds(nextFailedCredentialIds)
      if (taskResponse.data?.status === 'succeeded') {
        toast.success(t('Tested {{count}} keys', { count: results.length }))
      } else {
        toast.error(taskResponse.data?.error || t('Operation failed'))
      }
      await loadKeyStatus(1, pageSize, statusFilter)
    } catch (error: unknown) {
      if (!isCurrentChannel(channelId)) return
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    } finally {
      if (isCurrentChannel(channelId)) {
        setTestTaskId(null)
        setIsTestingKeys(false)
      }
    }
  }

  const stopKeyTest = async () => {
    if (!currentRow || !testTaskId) return
    const channelId = currentRow.id
    if (!isCurrentChannel(channelId)) return
    try {
      await cancelMultiKeyTestTask(channelId, testTaskId)
      if (!isCurrentChannel(channelId)) return
      toast.success(t('Stop requested'))
    } catch (error: unknown) {
      if (!isCurrentChannel(channelId)) return
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    }
  }

  const disableFailedKeys = async () => {
    if (!currentRow) return
    const channelId = currentRow.id
    if (!isCurrentChannel(channelId)) return
    const failedIds = failedCredentialIds
    if (failedIds.length === 0) {
      toast.error(t('No failed keys'))
      return
    }
    setIsPerformingAction(true)
    try {
      const response = await updateMultiKeyStatus(channelId, {
        credential_ids: failedIds,
        status: 'manual_disabled',
        reason: 'failed credential test',
        keys_revision: keysRevision,
      })
      if (!response.success) {
        throw new Error(response.message || t('Operation failed'))
      }
      if (!isCurrentChannel(channelId)) return
      toast.success(t('Disabled {{count}} keys', { count: failedIds.length }))
      await loadKeyStatus(currentPage, pageSize, statusFilter)
    } catch (error: unknown) {
      if (!isCurrentChannel(channelId)) return
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    } finally {
      if (isCurrentChannel(channelId)) setIsPerformingAction(false)
    }
  }

  const openProxyEditor = (key: KeyStatus) => {
    setProxyBatch(false)
    setProxyTarget(key)
    setProxyMode(
      key.proxy_mode === 'direct' || key.proxy_mode === 'custom'
        ? key.proxy_mode
        : 'inherit'
    )
    setProxyUrl('')
  }

  const openBatchProxyEditor = () => {
    if (selectedCredentialIds.length === 0) {
      toast.error(t('Select at least one key'))
      return
    }
    setProxyBatch(true)
    setProxyTarget(
      keys.find((key) => key.credential_id === selectedCredentialIds[0]) || null
    )
    setProxyMode('inherit')
    setProxyUrl('')
  }

  const saveProxy = async () => {
    if (!currentRow || (!proxyTarget?.credential_id && !proxyBatch)) return
    const channelId = currentRow.id
    if (!isCurrentChannel(channelId)) return
    setIsSavingProxy(true)
    try {
      const response = await updateMultiKeyProxy(channelId, {
        credential_id: proxyBatch ? undefined : proxyTarget?.credential_id,
        credential_ids: proxyBatch ? selectedCredentialIds : undefined,
        proxy_mode: proxyMode,
        proxy_url: proxyMode === 'custom' ? proxyUrl : undefined,
        keys_revision: keysRevision,
      })
      if (!isCurrentChannel(channelId)) return
      if (!response.success) {
        toast.error(response.message || t('Operation failed'))
        return
      }
      toast.success(t('Proxy updated'))
      setProxyTarget(null)
      await loadKeyStatus(currentPage, pageSize, statusFilter)
    } catch (error: unknown) {
      if (!isCurrentChannel(channelId)) return
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    } finally {
      if (isCurrentChannel(channelId)) setIsSavingProxy(false)
    }
  }

  const renderStatusBadge = (status: number) => {
    const config = getMultiKeyStatusConfig(status)
    return (
      <StatusBadge
        label={t(config.label)}
        variant={config.variant}
        showDot
        copyable={false}
      />
    )
  }

  const formatKeyTimestamp = (timestamp?: number) => {
    if (!timestamp) return '-'
    return formatTimestamp(timestamp)
  }

  const getTestResult = (key: KeyStatus) => {
    return getMultiKeyTestResult(testResults, key)
  }

  if (!currentRow) return null
  const visibleKeys = failedOnly
    ? keys.filter(
        (key) =>
          key.credential_id && failedCredentialIds.includes(key.credential_id)
      )
    : keys

  let multiKeyModeLabel = t('Polling')
  if (currentRow.channel_info?.multi_key_mode === 'random') {
    multiKeyModeLabel = t('Random')
  } else if (currentRow.channel_info?.multi_key_mode === 'affinity') {
    multiKeyModeLabel = t('Token Affinity')
  }

  return (
    <>
      <Dialog
        open={open}
        onOpenChange={onOpenChange}
        title={
          <>
            {t('Multi-Key Management')}
            <StatusBadge
              label={currentRow.name}
              variant='neutral'
              copyable={false}
            />
            {currentRow.channel_info?.multi_key_mode && (
              <StatusBadge
                label={multiKeyModeLabel}
                variant='neutral'
                copyable={false}
              />
            )}
          </>
        }
        description={t(
          'Manage multi-key status and configuration for this channel'
        )}
        contentClassName='flex max-h-[90vh] max-w-[min(96vw,1440px)] flex-col sm:max-w-[min(96vw,1440px)]'
        titleClassName='flex items-center gap-2'
        contentHeight='min(72vh, 720px)'
        bodyClassName='space-y-4'
      >
        <div className='flex min-h-0 flex-1 flex-col space-y-4 overflow-hidden'>
          {/* Statistics */}
          <div className='grid shrink-0 grid-cols-3 gap-3'>
            <StatisticsCard
              label={t('Enabled')}
              count={enabledCount}
              total={total}
            />
            <StatisticsCard
              label={t('Manual Disabled')}
              count={manualDisabledCount}
              total={total}
            />
            <StatisticsCard
              label={t('Auto Disabled')}
              count={autoDisabledCount}
              total={total}
            />
          </div>

          <Separator className='shrink-0' />

          {/* Toolbar */}
          <div className='flex shrink-0 items-center justify-between'>
            <Select
              items={MULTI_KEY_FILTER_OPTIONS.map((option) => ({
                value: option.value,
                label: t(option.label),
              }))}
              value={statusFilter === null ? 'all' : statusFilter.toString()}
              onValueChange={(v) => v !== null && handleStatusFilterChange(v)}
            >
              <SelectTrigger className='w-40'>
                <SelectValue placeholder={t('All Status')} />
              </SelectTrigger>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {MULTI_KEY_FILTER_OPTIONS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {t(option.label)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>

            <div className='flex items-center gap-2'>
              <Button
                variant='outline'
                size='sm'
                onClick={() => void loadKeyStatus()}
                disabled={isLoading || isTestingKeys}
              >
                <RefreshCw className='h-4 w-4' />
              </Button>

              <Button
                variant='outline'
                size='sm'
                onClick={() => void runKeyTest(false)}
                disabled={isTestingKeys}
              >
                <FlaskConical className='mr-2 h-4 w-4' />
                {isTestingKeys ? t('Testing...') : t('Test enabled')}
              </Button>
              <Button
                variant='outline'
                size='sm'
                onClick={() => void runKeyTest(true)}
                disabled={isTestingKeys}
              >
                <FlaskConical className='mr-2 h-4 w-4' />
                {t('Test all')}
              </Button>

              {isTestingKeys && (
                <Button
                  variant='destructive'
                  size='sm'
                  onClick={() => void stopKeyTest()}
                >
                  <Square className='mr-2 h-4 w-4' />
                  {t('Stop')}
                </Button>
              )}
              {selectedCredentialIds.length > 0 && !isTestingKeys && (
                <>
                  <Button
                    variant='outline'
                    size='sm'
                    onClick={() =>
                      setConfirmAction({
                        type: 'enable-selected',
                        credentialIds: selectedCredentialIds,
                      })
                    }
                  >
                    <Power className='mr-2 h-4 w-4' />
                    {t('Enable selected')}
                  </Button>
                  <Button
                    variant='outline'
                    size='sm'
                    onClick={() =>
                      setConfirmAction({
                        type: 'disable-selected',
                        credentialIds: selectedCredentialIds,
                      })
                    }
                  >
                    <PowerOff className='mr-2 h-4 w-4' />
                    {t('Disable selected')}
                  </Button>
                  <Button
                    variant='outline'
                    size='sm'
                    onClick={openBatchProxyEditor}
                  >
                    {t('Proxy selected')}
                  </Button>
                </>
              )}
              {Object.keys(testResults).length > 0 && !isTestingKeys && (
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => setFailedOnly((value) => !value)}
                >
                  {failedOnly ? t('Show all') : t('Failed only')}
                </Button>
              )}
              {Object.keys(testResults).length > 0 && !isTestingKeys && (
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => void disableFailedKeys()}
                >
                  {t('Disable failed')}
                </Button>
              )}

              {manualDisabledCount + autoDisabledCount > 0 && (
                <Button
                  variant='default'
                  size='sm'
                  onClick={() => setConfirmAction({ type: 'enable-all' })}
                >
                  <Power className='mr-2 h-4 w-4' />
                  {t('Enable All')}
                </Button>
              )}

              {enabledCount > 0 && (
                <Button
                  variant='destructive'
                  size='sm'
                  onClick={() => setConfirmAction({ type: 'disable-all' })}
                >
                  <PowerOff className='mr-2 h-4 w-4' />
                  {t('Disable All')}
                </Button>
              )}

              {autoDisabledCount > 0 && (
                <Button
                  variant='destructive'
                  size='sm'
                  onClick={() => {
                    if (!canEditSensitive) return
                    setConfirmAction({ type: 'delete-disabled' })
                  }}
                  disabled={!canEditSensitive}
                  title={
                    canEditSensitive
                      ? undefined
                      : t('No permission to perform this action')
                  }
                >
                  <Trash2 className='mr-2 h-4 w-4' />
                  {t('Delete Auto-Disabled')}
                </Button>
              )}
            </div>
          </div>
          {!canEditSensitive && (
            <p className='text-muted-foreground text-xs'>
              {t('No permission to perform this action')}
            </p>
          )}

          {/* Table */}
          <div className='min-h-0 flex-1 overflow-auto rounded-md border'>
            {isLoading && (
              <div className='flex items-center justify-center py-12'>
                <Loader2 className='text-muted-foreground h-8 w-8 animate-spin' />
              </div>
            )}
            {!isLoading && keys.length === 0 && (
              <div className='text-muted-foreground py-12 text-center'>
                {t('No keys found')}
              </div>
            )}
            {!isLoading && keys.length > 0 && (
              <StaticDataTable
                className='rounded-none border-0'
                tableClassName='min-w-[800px]'
                data={visibleKeys}
                getRowKey={(key, rowIndex) =>
                  `${key.credential_id ?? 'key'}-${getMultiKeyIndex(key, rowIndex)}`
                }
                columns={[
                  {
                    id: 'select',
                    header: (
                      <Checkbox
                        checked={
                          keys.length > 0 &&
                          keys.every(
                            (key) =>
                              key.credential_id &&
                              selectedCredentialIds.includes(key.credential_id)
                          )
                        }
                        onCheckedChange={(checked) => {
                          if (checked) {
                            setSelectedCredentialIds(
                              keys.flatMap((key) =>
                                key.credential_id ? [key.credential_id] : []
                              )
                            )
                          } else {
                            setSelectedCredentialIds([])
                          }
                        }}
                        aria-label={t('Select all')}
                      />
                    ),
                    className: 'w-10',
                    cell: (key) => (
                      <Checkbox
                        checked={Boolean(
                          key.credential_id &&
                          selectedCredentialIds.includes(key.credential_id)
                        )}
                        onCheckedChange={(checked) => {
                          if (!key.credential_id) return
                          setSelectedCredentialIds((current) =>
                            checked
                              ? [
                                  ...new Set([
                                    ...current,
                                    key.credential_id as number,
                                  ]),
                                ]
                              : current.filter((id) => id !== key.credential_id)
                          )
                        }}
                        aria-label={t('Select key {{index}}', {
                          index: getMultiKeyIndex(key) + 1,
                        })}
                      />
                    ),
                  },
                  {
                    id: 'index',
                    header: t('Index'),
                    className: 'w-20',
                    cellClassName: 'font-mono text-sm',
                    cell: (key) => `#${getMultiKeyIndex(key) + 1}`,
                  },
                  {
                    id: 'status',
                    header: t('Status'),
                    className: 'w-32',
                    cell: (key) => renderStatusBadge(key.status),
                  },
                  {
                    id: 'reason',
                    header: t('Disabled Reason'),
                    className: 'min-w-[200px]',
                    cellClassName: 'max-w-xs truncate text-sm',
                    cell: (key) => key.reason || '-',
                  },
                  {
                    id: 'fingerprint',
                    header: t('Fingerprint'),
                    className: 'w-40',
                    cellClassName: 'font-mono text-xs',
                    cell: (key) => key.fingerprint?.slice(0, 12) || '-',
                  },
                  {
                    id: 'proxy',
                    header: t('Proxy'),
                    className: 'min-w-[160px]',
                    cellClassName: 'text-muted-foreground text-xs',
                    cell: (key) =>
                      key.proxy_summary || key.proxy_mode || 'inherit',
                  },
                  {
                    id: 'test',
                    header: t('Last test'),
                    className: 'w-44',
                    cellClassName: 'text-xs',
                    cell: (key) => (
                      <div className='space-y-0.5'>
                        <div>
                          {formatMultiKeyTestResult(
                            getTestResult(key),
                            key,
                            t
                          ) || '-'}
                        </div>
                        <div className='text-muted-foreground'>
                          {formatKeyTimestamp(key.last_test_at)}
                        </div>
                      </div>
                    ),
                  },
                  {
                    id: '24h-requests',
                    header: t('24h requests'),
                    className: 'w-24',
                    cell: (key) => {
                      const metric = key.credential_id
                        ? keyMetrics[key.credential_id]
                        : undefined
                      return metric && metric.sample_sufficient
                        ? metric.request_count.toLocaleString()
                        : t('Insufficient sample')
                    },
                  },
                  {
                    id: '24h-success',
                    header: t('24h success'),
                    className: 'w-24',
                    cell: (key) => {
                      const metric = key.credential_id
                        ? keyMetrics[key.credential_id]
                        : undefined
                      return metric && metric.sample_sufficient
                        ? `${metric.request_success_rate.toFixed(1)}%`
                        : t('Insufficient sample')
                    },
                  },
                  {
                    id: '24h-p95',
                    header: t('24h P95'),
                    className: 'w-24',
                    cell: (key) => {
                      const metric = key.credential_id
                        ? keyMetrics[key.credential_id]
                        : undefined
                      return metric && metric.sample_sufficient
                        ? `${metric.p95_latency_ms} ms`
                        : t('Insufficient sample')
                    },
                  },
                  {
                    id: '24h-cache',
                    header: t('24h cache'),
                    className: 'w-24',
                    cell: (key) => {
                      const metric = key.credential_id
                        ? keyMetrics[key.credential_id]
                        : undefined
                      return metric && metric.usage_sufficient
                        ? `${metric.cache_hit_rate.toFixed(1)}%`
                        : t('Insufficient sample')
                    },
                  },
                  {
                    id: 'disabled-time',
                    header: t('Disabled Time'),
                    className: 'w-44',
                    cellClassName: 'text-muted-foreground text-sm',
                    cell: (key) => formatKeyTimestamp(key.disabled_time),
                  },
                  {
                    id: 'actions',
                    header: t('Actions'),
                    className: 'text-right',
                    cell: (key) => (
                      <MultiKeyTableRowActions
                        keyIndex={getMultiKeyIndex(key)}
                        status={key.status}
                        canDelete={canEditSensitive}
                        onAction={setConfirmAction}
                        onProxy={() => openProxyEditor(key)}
                      />
                    ),
                  },
                ]}
              />
            )}
          </div>

          {/* Pagination */}
          {totalPages > 1 && (
            <div className='flex shrink-0 items-center justify-between'>
              <div className='text-muted-foreground text-sm'>
                {t('Page {{current}} of {{total}}', {
                  current: currentPage,
                  total: totalPages,
                })}
              </div>
              <div className='flex gap-2'>
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => handlePageChange(currentPage - 1)}
                  disabled={currentPage === 1 || isLoading}
                >
                  {t('Previous')}
                </Button>
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => handlePageChange(currentPage + 1)}
                  disabled={currentPage >= totalPages || isLoading}
                >
                  {t('Next')}
                </Button>
              </div>
            </div>
          )}
        </div>
      </Dialog>

      {/* Confirmation Dialog */}
      <ConfirmDialog
        open={confirmAction !== null}
        onOpenChange={(open) => !open && setConfirmAction(null)}
        title={t('Confirm Action')}
        desc={t(getMultiKeyConfirmMessage(confirmAction))}
        destructive={isDestructiveAction(confirmAction)}
        isLoading={isPerformingAction}
        handleConfirm={performAction}
      />

      <Dialog
        open={proxyTarget !== null}
        onOpenChange={(open) => !open && setProxyTarget(null)}
        title={t('Key proxy')}
        description={t(
          'Set an independent proxy for this key. The existing URL is never returned.'
        )}
        contentClassName='sm:max-w-lg'
        contentHeight='auto'
        bodyClassName='space-y-4'
      >
        <Select
          items={[
            { value: 'inherit', label: t('Inherit channel proxy') },
            { value: 'direct', label: t('Direct connection') },
            { value: 'custom', label: t('Custom proxy') },
          ]}
          value={proxyMode}
          onValueChange={(value) =>
            value && setProxyMode(value as typeof proxyMode)
          }
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='inherit'>
              {t('Inherit channel proxy')}
            </SelectItem>
            <SelectItem value='direct'>{t('Direct connection')}</SelectItem>
            <SelectItem value='custom'>{t('Custom proxy')}</SelectItem>
          </SelectContent>
        </Select>
        {proxyMode === 'custom' && (
          <Input
            type='password'
            value={proxyUrl}
            onChange={(event) => setProxyUrl(event.target.value)}
            placeholder='http://user:password@host:port'
            autoComplete='new-password'
          />
        )}
        <div className='flex justify-end gap-2'>
          <Button variant='outline' onClick={() => setProxyTarget(null)}>
            {t('Cancel')}
          </Button>
          <Button
            onClick={() => void saveProxy()}
            disabled={
              isSavingProxy ||
              (proxyMode === 'custom' && proxyUrl.trim() === '')
            }
          >
            {isSavingProxy ? (
              <Loader2 className='mr-2 h-4 w-4 animate-spin' />
            ) : null}
            {t('Save')}
          </Button>
        </div>
      </Dialog>
    </>
  )
}
