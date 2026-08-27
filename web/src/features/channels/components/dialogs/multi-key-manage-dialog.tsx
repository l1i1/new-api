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
  Plus,
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
import { TruncatedCell } from '@/components/data-table/core/truncated-cell'
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
import { Textarea } from '@/components/ui/textarea'
import { TooltipProvider } from '@/components/ui/tooltip'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  appendMultiKeyCredentials,
  cancelMultiKeyTestTask,
  deleteDisabledMultiKeys,
  deleteMultiKeyCredentials,
  disableAllMultiKeys,
  disableMultiKey,
  enableAllMultiKeys,
  enableMultiKey,
  getAllChannelObservability,
  getMultiKeyStatus,
  getMultiKeyTestTask,
  deleteMultiKey,
  testMultiKeys,
  updateMultiKeyProxy,
  updateMultiKeyStatus,
} from '../../api'
import { MULTI_KEY_FILTER_OPTIONS } from '../../constants'
import {
  channelsQueryKeys,
  aggregateMultiKeyObservability,
  formatTimestamp,
  formatMultiKeyTestResultCompact,
  getMultiKeyIndex,
  getMultiKeyTestErrorDetail,
  getMultiKeyTestResult,
  getMultiKeyStatusConfig,
  getMultiKeyConfirmMessage,
  isDestructiveAction,
} from '../../lib'
import { parseMultiKeyCredentialText, toMultiKeyCredentialPayload } from '../../lib/multi-key-credentials'
import type {
  ChannelObservabilityResult,
  KeyStatus,
  MultiKeyConfirmAction,
  MultiKeyTestResult,
  MultiKeyTestTaskState,
} from '../../types'
import { useChannels } from '../channels-provider'
import { StatisticsCard } from './multi-key-statistics-card'
import { MultiKeyTableRowActions } from './multi-key-table-row-actions'

// Maximum keys accepted by a single credential-test task. Pools beyond this
// need page-scoped selections; the toolbar surfaces that guidance.
const MAX_KEYS_PER_TEST = 500
// Largest page fetch used by the client-side "failed only" filter.
const FAILED_ONLY_PAGE_SIZE = 100
// Observability metrics are re-fetched at most once per 30 seconds per channel.
const OBSERVABILITY_CACHE_TTL_MS = 30_000
// Pinned left columns are fixed widths; offsets accumulate in this order.
const PINNED_LEFT_OFFSETS: Record<string, number> = {
  select: 0,
  index: 40,
  status: 104,
}

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
  const [pageSize, setPageSize] = useState(50)
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
  const [testProgress, setTestProgress] = useState<MultiKeyTestTaskState | null>(
    null
  )
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
  const [addKeysOpen, setAddKeysOpen] = useState(false)
  const [newKeysText, setNewKeysText] = useState('')
  const [isAddingKeys, setIsAddingKeys] = useState(false)
  const activeChannelId = useRef<number | null>(null)
  const loadSequence = useRef(0)
  const observabilityCache = useRef<{
    channelId: number
    at: number
    items: ChannelObservabilityResult[]
  } | null>(null)

  const resetChannelState = () => {
    setIsLoading(false)
    setKeys([])
    setCurrentPage(1)
    setPageSize(50)
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
    setTestProgress(null)
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
    setAddKeysOpen(false)
    setNewKeysText('')
    setIsAddingKeys(false)
  }

  const isCurrentChannel = (channelId: number) =>
    activeChannelId.current === channelId

  // Reset and load data when dialog opens
  useEffect(() => {
    if (open && currentRow) {
      activeChannelId.current = currentRow.id
      loadSequence.current += 1
      resetChannelState()
      void loadKeyStatus(1, 50, null)
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
        setPageSize(response.data.page_size || 50)
        setTotalPages(response.data.total_pages || 0)
        setEnabledCount(response.data.enabled_count || 0)
        setManualDisabledCount(response.data.manual_disabled_count || 0)
        setAutoDisabledCount(response.data.auto_disabled_count || 0)
        setKeysRevision(response.data.keys_revision)

        const cached = observabilityCache.current
        if (
          cached &&
          cached.channelId === channelId &&
          Date.now() - cached.at < OBSERVABILITY_CACHE_TTL_MS
        ) {
          setKeyMetrics(aggregateMultiKeyObservability(cached.items))
          return
        }
        try {
          const metricsItems = await getAllChannelObservability(channelId, 24)
          if (!isCurrentLoad()) return
          observabilityCache.current = {
            channelId,
            at: Date.now(),
            items: metricsItems,
          }
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
        confirmAction.type === 'delete-selected' ||
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
        response = await enableMultiKey(channelId, keyIndex, keysRevision)
      } else if (type === 'disable' && keyIndex !== undefined) {
        response = await disableMultiKey(channelId, keyIndex, keysRevision)
      } else if (type === 'delete' && keyIndex !== undefined) {
        response = await deleteMultiKey(channelId, keyIndex, keysRevision)
      } else if (type === 'delete-selected') {
        response = await deleteMultiKeyCredentials(
          channelId,
          confirmAction.credentialIds || [],
          keysRevision
        )
      } else if (type === 'enable-all') {
        response = await enableAllMultiKeys(channelId, keysRevision)
      } else if (type === 'disable-all') {
        response = await disableAllMultiKeys(channelId, keysRevision)
      } else if (type === 'delete-disabled') {
        response = await deleteDisabledMultiKeys(channelId, keysRevision)
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

    // Without an explicit selection, "test enabled" covers the whole enabled
    // pool (not just the visible page), and "test all" covers every key.
    let selectedIds: number[] | undefined
    const hasSelection = selectedCredentialIds.length > 0
    if (hasSelection) {
      selectedIds = selectedCredentialIds
    } else if (!all && enabledCount === 0) {
      toast.error(t('No enabled keys to test'))
      return
    }
    const requestedKeyCount = selectedIds
      ? selectedIds.length
      : all
        ? Math.max(total, 1)
        : Math.max(enabledCount, 1)
    if (requestedKeyCount > MAX_KEYS_PER_TEST) {
      toast.error(
        t('A test task supports at most {{count}} keys; select a smaller group', {
          count: MAX_KEYS_PER_TEST,
        })
      )
      return
    }

    setIsTestingKeys(true)
    setTestProgress(null)
    let keepTaskVisible = false
    try {
      const response = await testMultiKeys(channelId, {
        all: !hasSelection,
        include_disabled: hasSelection ? true : all,
        credential_ids: selectedIds,
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
      const estimatedBatches = Math.ceil(requestedKeyCount / 4)
      const deadline =
        Date.now() + Math.max(120_000, estimatedBatches * 60_000 + 30_000)
      while (
        taskResponse.data &&
        (taskResponse.data.status === 'pending' ||
          taskResponse.data.status === 'running') &&
        Date.now() < deadline
      ) {
        if (taskResponse.data.state) {
          setTestProgress(taskResponse.data.state)
        }
        await new Promise((resolve) => window.setTimeout(resolve, 500))
        if (!isCurrentChannel(channelId)) return
        taskResponse = await getMultiKeyTestTask(channelId, taskId)
      }
      if (taskResponse.data?.state) {
        setTestProgress(taskResponse.data.state)
      }
      if (!isCurrentChannel(channelId)) return
      if (
        taskResponse.data &&
        (taskResponse.data.status === 'pending' ||
          taskResponse.data.status === 'running')
      ) {
        keepTaskVisible = true
        toast.error(t('Test is still running in the background'))
        return
      }
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
      await loadKeyStatus(currentPage, pageSize, statusFilter)
    } catch (error: unknown) {
      if (!isCurrentChannel(channelId)) return
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    } finally {
      if (isCurrentChannel(channelId)) {
        if (!keepTaskVisible) {
          setTestTaskId(null)
          setTestProgress(null)
          setIsTestingKeys(false)
        }
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
      setTestTaskId(null)
      setTestProgress(null)
      setIsTestingKeys(false)
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
      setFailedOnly(false)
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

  const toggleFailedOnly = async () => {
    const next = !failedOnly
    setFailedOnly(next)
    // The "failed only" view is a client-side filter over a single expanded
    // fetch, so failures on any page remain visible.
    await loadKeyStatus(1, FAILED_ONLY_PAGE_SIZE, null)
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

  const confirmAddKeys = async () => {
    if (!currentRow) return
    const channelId = currentRow.id
    if (!isCurrentChannel(channelId)) return
    const parsed = parseMultiKeyCredentialText(newKeysText)
    if (parsed.length === 0) {
      toast.error(t('Enter at least one key'))
      return
    }
    setIsAddingKeys(true)
    try {
      const response = await appendMultiKeyCredentials(channelId, {
        credentials: toMultiKeyCredentialPayload(parsed),
        keys_revision: keysRevision,
      })
      if (!isCurrentChannel(channelId)) return
      if (!response.success) {
        toast.error(response.message || t('Operation failed'))
        return
      }
      toast.success(
        t('Added {{count}} keys', { count: parsed.length })
      )
      setAddKeysOpen(false)
      setNewKeysText('')
      observabilityCache.current = null
      await loadKeyStatus(currentPage, pageSize, statusFilter)
    } catch (error: unknown) {
      if (!isCurrentChannel(channelId)) return
      toast.error(
        error instanceof Error ? error.message : t('Operation failed')
      )
    } finally {
      if (isCurrentChannel(channelId)) setIsAddingKeys(false)
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

  // Parse preview for the add-keys dialog: how many secrets and proxies were
  // recognised with the same line-oriented grammar the channel editor uses.
  const parsedNewKeys = parseMultiKeyCredentialText(newKeysText)
  const parsedNewKeysWithProxy = parsedNewKeys.filter(
    (credential) => credential.proxyUrl != null
  ).length

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

  const testProgressPercent =
    testProgress && testProgress.total > 0
      ? Math.min(100, Math.round((testProgress.processed / testProgress.total) * 100))
      : null

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
        bodyClassName='flex min-h-0 flex-1 flex-col overflow-hidden'
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
          <div className='flex shrink-0 flex-wrap items-center justify-between gap-2'>
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

            <div className='flex flex-wrap items-center gap-2'>
              <Button
                variant='outline'
                size='sm'
                onClick={() => void loadKeyStatus()}
                disabled={isLoading || isTestingKeys}
              >
                <RefreshCw className='h-4 w-4' />
              </Button>

              {canEditSensitive && (
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => setAddKeysOpen(true)}
                >
                  <Plus className='mr-2 h-4 w-4' />
                  {t('Add keys')}
                </Button>
              )}

              <Button
                variant='outline'
                size='sm'
                onClick={() => void runKeyTest(false)}
                disabled={isTestingKeys || enabledCount === 0}
                title={enabledCount === 0 ? t('No enabled keys to test') : undefined}
              >
                <FlaskConical className='mr-2 h-4 w-4' />
                {isTestingKeys
                  ? testProgress
                    ? t('Testing {{processed}}/{{total}}', {
                        processed: testProgress.processed,
                        total: testProgress.total,
                      })
                    : t('Testing...')
                  : t('Test enabled')}
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

              {testProgressPercent !== null && (
                <div className='bg-muted h-1 w-24 overflow-hidden rounded-full'>
                  <div
                    className='bg-primary h-full transition-all'
                    style={{ width: `${testProgressPercent}%` }}
                  />
                </div>
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
                  <Button
                    variant='destructive'
                    size='sm'
                    onClick={() => {
                      if (!canEditSensitive) return
                      setConfirmAction({
                        type: 'delete-selected',
                        credentialIds: selectedCredentialIds,
                      })
                    }}
                    disabled={!canEditSensitive}
                    title={
                      canEditSensitive
                        ? undefined
                        : t('No permission to perform this action')
                    }
                  >
                    <Trash2 className='mr-2 h-4 w-4' />
                    {t('Delete selected')}
                  </Button>
                </>
              )}
              {Object.keys(testResults).length > 0 && !isTestingKeys && (
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => void toggleFailedOnly()}
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

          {/* Table: one bounded scroll container owns both axes, so the
              horizontal scrollbar stays at the viewport edge instead of the
              content bottom. Key columns are pinned on the left and actions
              on the right. */}
          <div className='multi-key-scrollbar max-h-90 min-h-0 flex-1 overflow-auto rounded-md border'>
            <TooltipProvider delay={150}>
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
                  className='overflow-visible rounded-none border-0'
                  tableClassName='min-w-[1288px]'
                  tableContainerClassName='overflow-visible'
                  headerRowClassName='[&_th]:sticky [&_th]:top-0 [&_th]:z-30 [&_th]:bg-[var(--table-header)]'
                  data={visibleKeys}
                  getRowKey={(key, rowIndex) =>
                    `${key.credential_id ?? 'key'}-${getMultiKeyIndex(key, rowIndex)}`
                  }
                  columns={[
                    {
                      id: 'select',
                      stickyLeft: PINNED_LEFT_OFFSETS.select,
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
                      stickyLeft: PINNED_LEFT_OFFSETS.index,
                      header: t('Index'),
                      className: 'w-16',
                      cellClassName: 'font-mono text-sm',
                      cell: (key) => `#${getMultiKeyIndex(key) + 1}`,
                    },
                    {
                      id: 'status',
                      stickyLeft: PINNED_LEFT_OFFSETS.status,
                      header: t('Status'),
                      className: 'w-28',
                      cell: (key) => renderStatusBadge(key.status),
                    },
                    {
                      id: 'reason',
                      header: t('Disabled Reason'),
                      className: 'w-52',
                      cellClassName: 'max-w-[13rem] text-sm',
                      cell: (key) =>
                        key.reason ? (
                          <TruncatedCell tooltipContent={key.reason}>
                            <span className='text-muted-foreground'>
                              {key.reason}
                            </span>
                          </TruncatedCell>
                        ) : (
                          <span className='text-muted-foreground'>-</span>
                        ),
                    },
                    {
                      id: 'fingerprint',
                      header: t('Fingerprint'),
                      className: 'w-24',
                      cellClassName: 'font-mono text-xs',
                      cell: (key) =>
                        key.fingerprint?.slice(0, 12) || '-',
                    },
                    {
                      id: 'proxy',
                      header: t('Proxy'),
                      className: 'w-36',
                      cellClassName: 'text-muted-foreground text-xs',
                      cell: (key) =>
                        key.proxy_summary || key.proxy_mode || 'inherit',
                    },
                    {
                      id: 'test',
                      header: t('Last test'),
                      className: 'w-28',
                      cellClassName: 'text-xs',
                      cell: (key) => {
                        const result = getTestResult(key)
                        const detail = getMultiKeyTestErrorDetail(result, key)
                        const label = formatMultiKeyTestResultCompact(
                          result,
                          key,
                          t
                        )
                        if (!label) return '-'
                        const isFail =
                          (result?.status ?? key.last_test_status) === 'failed'
                        return (
                          <TruncatedCell tooltipContent={detail}>
                            <span
                              className={isFail ? 'text-destructive' : undefined}
                            >
                              {label}
                            </span>
                          </TruncatedCell>
                        )
                      },
                    },
                    {
                      id: 'test-time',
                      header: t('Last test time'),
                      className: 'w-28',
                      cellClassName: 'text-muted-foreground text-xs',
                      cell: (key) => formatKeyTimestamp(key.last_test_at),
                    },
                    {
                      id: 'metrics',
                      header: t('24h metrics'),
                      className: 'w-36',
                      cellClassName: 'text-xs',
                      cell: (key) => {
                        const metric = key.credential_id
                          ? keyMetrics[key.credential_id]
                          : undefined
                        if (!metric) {
                          return <span className='text-muted-foreground'>–</span>
                        }
                        if (!metric.sample_sufficient) {
                          return (
                            <TruncatedCell tooltipContent={t('Insufficient sample')}>
                              <span className='text-muted-foreground'>–</span>
                            </TruncatedCell>
                          )
                        }
                        const cacheRate = metric.usage_sufficient
                          ? `${metric.cache_hit_rate.toFixed(1)}%`
                          : t('Insufficient sample')
                        return (
                          <TruncatedCell
                            tooltipContent={[
                              t('24h requests'),
                              metric.request_count.toLocaleString(),
                              `${t('24h success')}: ${metric.request_success_rate.toFixed(1)}%`,
                              `${t('24h P95')}: ${metric.p95_latency_ms} ms`,
                              `${t('24h cache')}: ${cacheRate}`,
                            ].join('\n')}
                          >
                            <div className='space-y-0.5'>
                              <div>
                                {metric.request_count.toLocaleString()}{' '}
                                · {metric.request_success_rate.toFixed(1)}%
                              </div>
                              <div className='text-muted-foreground'>
                                {metric.p95_latency_ms} ms ·{' '}
                                {metric.usage_sufficient
                                  ? `${metric.cache_hit_rate.toFixed(1)}%`
                                  : t('Insufficient sample')}
                              </div>
                            </div>
                          </TruncatedCell>
                        )
                      },
                    },
                    {
                      id: 'disabled-time',
                      header: t('Disabled Time'),
                      className: 'w-32',
                      cellClassName: 'text-muted-foreground text-sm',
                      cell: (key) => formatKeyTimestamp(key.disabled_time),
                    },
                    {
                      id: 'actions',
                      stickyRight: true,
                      header: t('Actions'),
                      className: 'w-32 text-right',
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
            </TooltipProvider>
          </div>

          {/* Pagination */}
          {(totalPages > 1 || total > 0) && (
            <div className='flex shrink-0 items-center justify-between'>
              <div className='text-muted-foreground text-sm'>
                {t('Page {{current}} of {{total}}', {
                  current: currentPage,
                  total: totalPages,
                })}
              </div>
              <div className='flex items-center gap-2'>
                <Select
                  items={[25, 50, 100].map((value) => ({
                    value: `${value}`,
                    label: `${value}`,
                  }))}
                  value={`${pageSize}`}
                  onValueChange={(value) => {
                    const nextPageSize = Number(value)
                    setPageSize(nextPageSize)
                    setCurrentPage(1)
                    void loadKeyStatus(1, nextPageSize, statusFilter)
                  }}
                >
                  <SelectTrigger className='h-8 w-20'>
                    <SelectValue placeholder={pageSize} />
                  </SelectTrigger>
                  <SelectContent side='top' alignItemWithTrigger={false}>
                    <SelectGroup>
                      {[25, 50, 100].map((value) => (
                        <SelectItem key={value} value={`${value}`}>
                          {value}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
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

      {/* Add keys dialog */}
      <Dialog
        open={addKeysOpen}
        onOpenChange={(open) => {
          setAddKeysOpen(open)
          if (!open) setNewKeysText('')
        }}
        title={t('Add keys')}
        description={t(
          'New keys are appended to the end of the key list. Enter one key per line; an optional proxy URL on the next non-empty line belongs to that key.'
        )}
        contentClassName='sm:max-w-lg'
        contentHeight='auto'
        bodyClassName='space-y-4'
      >
        <Textarea
          value={newKeysText}
          onChange={(event) => setNewKeysText(event.target.value)}
          rows={8}
          autoComplete='new-password'
        />
        {newKeysText.trim() !== '' && (
          <p className='text-muted-foreground text-xs'>
            {parsedNewKeys.length > 0
              ? t('Parsed {{count}} keys ({{proxies}} with proxy)', {
                  count: parsedNewKeys.length,
                  proxies: parsedNewKeysWithProxy,
                })
              : t('No valid keys recognized')}
          </p>
        )}
        <div className='flex justify-end gap-2'>
          <Button variant='outline' onClick={() => setAddKeysOpen(false)}>
            {t('Cancel')}
          </Button>
          <Button
            onClick={() => void confirmAddKeys()}
            disabled={
              isAddingKeys ||
              parsedNewKeys.length === 0 ||
              !canEditSensitive
            }
          >
            {isAddingKeys ? (
              <Loader2 className='mr-2 h-4 w-4 animate-spin' />
            ) : null}
            {t('Add keys')}
          </Button>
        </div>
      </Dialog>

      {/* Proxy dialog */}
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
