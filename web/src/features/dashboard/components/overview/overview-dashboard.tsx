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
import { Link } from '@tanstack/react-router'
import {
  ArrowRight,
  BookOpen,
  Check,
  ChevronDown,
  ChevronUp,
  Circle,
  Copy,
  CreditCard,
  FileText,
  KeyRound,
  RadioTower,
  TerminalSquare,
  type LucideIcon,
} from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  CardStaggerContainer,
  CardStaggerItem,
} from '@/components/page-transition'
import { Button } from '@/components/ui/button'
import { fetchTokenKey, getApiKeys } from '@/features/keys/api'
import type { ApiKey } from '@/features/keys/types'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { getUserModels } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { getUptimeStatus } from '../../api'
import {
  useApiInfo,
  useDashboardContentVisibility,
} from '../../hooks/use-status-data'
import type { UptimeGroupResult } from '../../types'
import { AnnouncementsPanel } from './announcements-panel'
import { FAQPanel } from './faq-panel'
import { PerformanceHealthPanel } from './performance-health-panel'
import { SummaryCards } from './summary-cards'
import { UptimePanel } from './uptime-panel'

const SETUP_GUIDE_VISIBILITY_STORAGE_KEY =
  'dashboard_overview_setup_guide_expanded'

type DashboardActionPath =
  | '/keys'
  | '/wallet'
  | '/playground'
  | '/channels'
  | '/usage-logs'
  | '/pricing'

interface StartStep {
  title: string
  description: string
  to: DashboardActionPath
  icon: LucideIcon
  completed: boolean
}

interface QuickAction {
  title: string
  to: DashboardActionPath
  icon: LucideIcon
  adminOnly?: boolean
}

interface RequestExample {
  endpoint: string
  model: string
  keyName: string
  keyId?: number
  displayKey: string
  ready: boolean
}

interface StatusSignal {
  label: string
  value: string
}

function getSavedSetupGuideExpanded(): boolean | null {
  if (typeof window === 'undefined') return null
  const saved = window.localStorage.getItem(SETUP_GUIDE_VISIBILITY_STORAGE_KEY)
  if (saved === 'expanded') return true
  if (saved === 'collapsed') return false
  return null
}

function saveSetupGuideExpanded(expanded: boolean): void {
  if (typeof window === 'undefined') return
  window.localStorage.setItem(
    SETUP_GUIDE_VISIBILITY_STORAGE_KEY,
    expanded ? 'expanded' : 'collapsed'
  )
}

function getCurrentOrigin(): string {
  if (typeof window === 'undefined') return ''
  return window.location.origin
}

function normalizeEndpoint(sourceUrl?: string): string {
  const fallback = `${getCurrentOrigin()}/v1/chat/completions`
  const trimmed = sourceUrl?.trim()
  if (!trimmed) return fallback

  const withoutTrailingSlash = trimmed.replace(/\/+$/, '')
  if (withoutTrailingSlash.endsWith('/v1/chat/completions')) {
    return withoutTrailingSlash
  }
  if (withoutTrailingSlash.endsWith('/v1')) {
    return `${withoutTrailingSlash}/chat/completions`
  }
  return `${withoutTrailingSlash}/v1/chat/completions`
}

function getPreferredKey(keys: ApiKey[]): ApiKey | null {
  return keys.find((item) => item.status === 1) ?? keys[0] ?? null
}

function formatDisplayKey(key?: string): string {
  if (!key) return 'sk-...'
  if (key.length <= 14) return key
  return `${key.slice(0, 7)}...${key.slice(-4)}`
}

function buildCurlCommand(args: {
  endpoint: string
  apiKey: string
  model: string
}): string {
  return [
    `curl ${args.endpoint} \\`,
    '  -H "Content-Type: application/json" \\',
    `  -H "Authorization: Bearer ${args.apiKey}" \\`,
    `  -d '{"model":"${args.model}","messages":[{"role":"user","content":"Say hello in one sentence."}]}'`,
  ].join('\n')
}

function SetupStepRow(props: { step: StartStep; index: number }) {
  const StatusIcon = props.step.completed ? Check : Circle

  return (
    <li>
      <Link
        to={props.step.to}
        className='hover:bg-muted/50 focus-visible:ring-ring group flex items-center gap-3 px-4 py-3 outline-none focus-visible:ring-2 focus-visible:ring-inset sm:px-5'
      >
        <StatusIcon
          className={cn(
            'size-4 shrink-0',
            props.step.completed ? 'text-success' : 'text-muted-foreground/40'
          )}
          aria-hidden='true'
        />
        <span className='flex min-w-0 flex-1 flex-col gap-0.5'>
          <span
            className={cn(
              'truncate text-sm font-medium',
              props.step.completed && 'text-muted-foreground'
            )}
          >
            <span className='text-muted-foreground mr-1.5 font-mono text-xs tabular-nums'>
              {props.index + 1}.
            </span>
            {props.step.title}
          </span>
          <span className='text-muted-foreground truncate text-xs'>
            {props.step.description}
          </span>
        </span>
        <ArrowRight
          className='text-muted-foreground size-4 shrink-0 transition-transform group-hover:translate-x-0.5'
          aria-hidden='true'
        />
      </Link>
    </li>
  )
}

function FirstRequestPanel(props: {
  example: RequestExample
  signals: StatusSignal[]
}) {
  const { t } = useTranslation()
  const [isCopying, setIsCopying] = useState(false)
  const { copyToClipboard } = useCopyToClipboard({ notify: false })
  const previewCurl = buildCurlCommand({
    endpoint: props.example.endpoint,
    apiKey: props.example.displayKey,
    model: props.example.model,
  })

  const handleCopyRequest = async () => {
    if (!props.example.keyId || isCopying) return

    setIsCopying(true)
    try {
      const result = await fetchTokenKey(props.example.keyId)
      const key = result.success && result.data?.key ? result.data.key : ''
      if (!key) {
        toast.error(result.message || t('Failed to copy to clipboard'))
        return
      }

      const realCurl = buildCurlCommand({
        endpoint: props.example.endpoint,
        apiKey: `sk-${key}`,
        model: props.example.model,
      })
      const copied = await copyToClipboard(realCurl)
      if (copied) {
        toast.success(t('Copied to clipboard'))
      } else {
        toast.error(t('Failed to copy to clipboard'))
      }
    } finally {
      setIsCopying(false)
    }
  }

  return (
    <div className='border-border flex flex-col border-t lg:border-t-0'>
      <div className='flex items-center justify-between gap-3 px-4 pt-3.5 sm:px-5'>
        <div className='min-w-0'>
          <div className='truncate text-sm font-medium'>
            {t('First API request')}
          </div>
          <div className='text-muted-foreground truncate text-xs'>
            {props.example.ready
              ? props.example.keyName
              : t('Create an API key to unlock the real request')}
          </div>
        </div>
        {props.example.ready ? (
          <Button
            variant='outline'
            size='sm'
            className='h-7 shrink-0 gap-1.5 px-2 text-xs'
            disabled={isCopying}
            onClick={handleCopyRequest}
            aria-label={t('Copy ready-to-run curl')}
          >
            <Copy data-icon='inline-start' />
            {isCopying ? t('Loading') : t('Copy')}
          </Button>
        ) : (
          <Button
            size='sm'
            variant='outline'
            className='h-7 shrink-0 px-2 text-xs'
            render={<Link to='/keys' />}
          >
            {t('Create API Key')}
          </Button>
        )}
      </div>

      <pre className='bg-muted/40 border-border text-muted-foreground mx-4 mt-3 overflow-x-auto border p-3 font-mono text-xs leading-relaxed whitespace-pre sm:mx-5'>
        {previewCurl}
      </pre>

      <div className='flex flex-wrap gap-x-4 gap-y-1 px-4 py-3 sm:px-5'>
        {props.signals.map((signal) => (
          <span key={signal.label} className='text-muted-foreground text-xs'>
            {signal.label}
            {': '}
            <span className='text-foreground font-medium'>{signal.value}</span>
          </span>
        ))}
      </div>
    </div>
  )
}

function QuickActionLink(props: { action: QuickAction }) {
  const Icon = props.action.icon

  return (
    <Link
      to={props.action.to}
      className='text-foreground hover:text-primary focus-visible:ring-ring inline-flex items-center gap-1.5 rounded-none text-xs font-medium outline-none focus-visible:ring-2'
    >
      <Icon className='text-muted-foreground size-3.5' aria-hidden='true' />
      {props.action.title}
    </Link>
  )
}

export function OverviewDashboard() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const { items: apiInfoItems } = useApiInfo()
  const {
    announcements: showAnnouncementsPanel,
    faq: showFAQPanel,
    uptimeKuma: showUptimePanel,
  } = useDashboardContentVisibility()
  const [manualSetupGuideExpanded, setManualSetupGuideExpanded] = useState<
    boolean | null
  >(() => getSavedSetupGuideExpanded())

  const requestCount = Number(user?.request_count ?? 0)
  const remainQuota = Number(user?.quota ?? 0)
  const usedQuota = Number(user?.used_quota ?? 0)
  const isAdmin = Boolean(user?.role && user.role >= ROLE.ADMIN)

  const apiKeysQuery = useQuery({
    queryKey: ['dashboard', 'overview', 'api-keys'],
    queryFn: async () => {
      const result = await getApiKeys({ p: 1, size: 10 })
      return result.success ? (result.data?.items ?? []) : []
    },
    staleTime: 60 * 1000,
  })

  const modelsQuery = useQuery({
    queryKey: ['dashboard', 'overview', 'user-models'],
    queryFn: async () => {
      const result = await getUserModels()
      return result.success ? (result.data ?? []) : []
    },
    staleTime: 5 * 60 * 1000,
  })

  const uptimeQuery = useQuery({
    queryKey: ['dashboard', 'overview', 'uptime-status'],
    queryFn: async (): Promise<UptimeGroupResult[]> => {
      const result = await getUptimeStatus()
      return result?.data ?? []
    },
    enabled: showUptimePanel,
    staleTime: 60 * 1000,
    retry: false,
  })

  const preferredKey = useMemo(
    () => getPreferredKey(apiKeysQuery.data ?? []),
    [apiKeysQuery.data]
  )

  const startSteps = useMemo<StartStep[]>(
    () => [
      {
        title: t('Create API Key'),
        description: t('Create a key for your app or service'),
        to: '/keys',
        icon: KeyRound,
        completed: Boolean(preferredKey),
      },
      {
        title: t('Add credits'),
        description: t('Keep enough balance before production traffic'),
        to: '/wallet',
        icon: CreditCard,
        completed: remainQuota > 0 || usedQuota > 0,
      },
      {
        title: t('Send a request'),
        description: t('Verify routing with Playground or your client'),
        to: '/playground',
        icon: TerminalSquare,
        completed: requestCount > 0,
      },
    ],
    [preferredKey, remainQuota, requestCount, t, usedQuota]
  )

  const quickActions = useMemo<QuickAction[]>(
    () => [
      {
        title: t('API Keys'),
        to: '/keys',
        icon: KeyRound,
      },
      {
        title: t('Channels'),
        to: '/channels',
        icon: RadioTower,
        adminOnly: true,
      },
      {
        title: t('Usage Logs'),
        to: '/usage-logs',
        icon: FileText,
      },
      {
        title: t('Pricing'),
        to: '/pricing',
        icon: BookOpen,
      },
    ],
    [t]
  )

  const visibleQuickActions = useMemo(
    () => quickActions.filter((action) => !action.adminOnly || isAdmin),
    [isAdmin, quickActions]
  )

  const statusSignals = useMemo<StatusSignal[]>(
    () => [
      {
        label: t('Route active'),
        value: apiInfoItems.length > 0 ? t('Online') : t('Current domain'),
      },
      {
        label: t('Auth configured'),
        value: preferredKey ? t('Secured') : t('Needs API key'),
      },
      {
        label: t('Model selected'),
        value: modelsQuery.data?.[0] ?? t('Loading'),
      },
    ],
    [apiInfoItems.length, modelsQuery.data, preferredKey, t]
  )

  const requestExample = useMemo<RequestExample>(() => {
    const endpoint = normalizeEndpoint(apiInfoItems[0]?.url)
    const model = modelsQuery.data?.[0] ?? 'gpt-4o-mini'
    const keyName = preferredKey?.name ?? t('No API key yet')
    const ready = Boolean(preferredKey?.id && model)

    return {
      endpoint,
      model,
      keyName,
      keyId: preferredKey?.id,
      displayKey: preferredKey
        ? formatDisplayKey(`sk-${preferredKey.key}`)
        : 'sk-...',
      ready,
    }
  }, [apiInfoItems, modelsQuery.data, preferredKey, t])

  const completedStepCount = startSteps.filter((step) => step.completed).length
  const setupComplete = completedStepCount === startSteps.length
  const setupStatusReady = apiKeysQuery.isFetched && Boolean(user)
  const setupGuideExpanded =
    manualSetupGuideExpanded ?? (setupStatusReady && !setupComplete)
  const showLeftContentPanels =
    isAdmin || showAnnouncementsPanel || showFAQPanel
  // Only reserve layout space for the uptime rail once monitors are actually
  // configured; an enabled-but-empty Uptime Kuma integration stays hidden.
  const uptimeGroups = uptimeQuery.data ?? []
  const showUptimeData = showUptimePanel && uptimeGroups.length > 0
  const showContentPanels = showLeftContentPanels || showUptimeData

  const setupProgressLabel = t('Setup progress: {{completed}}/{{total}}', {
    completed: completedStepCount,
    total: startSteps.length,
  })

  const handleSetupGuideToggle = () => {
    const nextExpanded = !setupGuideExpanded
    setManualSetupGuideExpanded(nextExpanded)
    saveSetupGuideExpanded(nextExpanded)
  }

  // Collapse once when completion is first confirmed, including when the
  // dashboard loads after the final step was completed elsewhere.
  const wasSetupCompleteRef = useRef<boolean | null>(null)
  useEffect(() => {
    if (!setupStatusReady) return
    if (
      wasSetupCompleteRef.current !== true &&
      setupComplete &&
      setupGuideExpanded
    ) {
      setManualSetupGuideExpanded(false)
      saveSetupGuideExpanded(false)
    }
    wasSetupCompleteRef.current = setupComplete
  }, [setupComplete, setupGuideExpanded, setupStatusReady])

  return (
    <div className='flex flex-col gap-4'>
      {setupGuideExpanded ? (
        <section className='bg-card border-border border'>
          <div className='border-border flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5 sm:px-5'>
            <div className='flex items-baseline gap-2'>
              <h2 className='text-sm font-semibold'>{t('Get started')}</h2>
              <span className='text-muted-foreground text-xs tabular-nums'>
                {setupProgressLabel}
              </span>
            </div>
            <div className='flex items-center gap-2'>
              <Button
                variant='ghost'
                size='sm'
                className='h-7 px-2 text-xs'
                onClick={handleSetupGuideToggle}
              >
                <ChevronUp data-icon='inline-start' />
                {t('Hide setup guide')}
              </Button>
              <Button
                size='sm'
                className='h-7 px-2.5 text-xs'
                render={<Link to='/keys' />}
              >
                <KeyRound data-icon='inline-start' />
                {t('Create API Key')}
              </Button>
            </div>
          </div>

          <div className='grid lg:grid-cols-2'>
            <ol className='divide-border divide-y'>
              {startSteps.map((step, index) => (
                <SetupStepRow key={step.title} step={step} index={index} />
              ))}
            </ol>
            <FirstRequestPanel
              example={requestExample}
              signals={statusSignals}
            />
          </div>

          <div className='border-border flex flex-wrap items-center gap-x-5 gap-y-2 border-t px-4 py-2.5 sm:px-5'>
            <span className='text-muted-foreground text-xs'>
              {t('Quick actions')}
            </span>
            {visibleQuickActions.map((action) => (
              <QuickActionLink key={action.title} action={action} />
            ))}
          </div>
        </section>
      ) : (
        <div className='bg-card border-border flex flex-wrap items-center justify-between gap-3 border px-4 py-2.5 sm:px-5'>
          <div className='flex min-w-0 items-center gap-2.5'>
            {setupComplete ? (
              <Check
                className='text-success size-4 shrink-0'
                aria-hidden='true'
              />
            ) : (
              <Circle
                className='text-muted-foreground/40 size-4 shrink-0'
                aria-hidden='true'
              />
            )}
            <span className='truncate text-sm font-medium'>
              {setupComplete ? t('Setup guide complete') : t('Setup guide')}
            </span>
            <span className='text-muted-foreground text-xs tabular-nums'>
              {setupProgressLabel}
            </span>
          </div>
          <Button
            variant='ghost'
            size='sm'
            className='h-7 px-2 text-xs'
            onClick={handleSetupGuideToggle}
          >
            <ChevronDown data-icon='inline-start' />
            {t('Show setup guide')}
          </Button>
        </div>
      )}

      <SummaryCards />

      {showContentPanels && (
        <CardStaggerContainer
          className={cn(
            'grid grid-cols-1 gap-4',
            showLeftContentPanels &&
              showUptimeData &&
              'xl:grid-cols-[minmax(0,1fr)_22rem]'
          )}
        >
          {showLeftContentPanels && (
            <div
              className={cn(
                'grid min-w-0 grid-cols-1 gap-4',
                (showAnnouncementsPanel || showFAQPanel) && 'lg:grid-cols-2'
              )}
            >
              {isAdmin && (
                <CardStaggerItem className='lg:col-span-2'>
                  <PerformanceHealthPanel />
                </CardStaggerItem>
              )}
              {showAnnouncementsPanel && (
                <CardStaggerItem>
                  <AnnouncementsPanel />
                </CardStaggerItem>
              )}
              {showFAQPanel && (
                <CardStaggerItem>
                  <FAQPanel />
                </CardStaggerItem>
              )}
            </div>
          )}
          {showUptimeData && (
            <CardStaggerItem>
              <UptimePanel
                groups={uptimeGroups}
                refreshing={uptimeQuery.isRefetching}
                onRefresh={() => void uptimeQuery.refetch()}
              />
            </CardStaggerItem>
          )}
        </CardStaggerContainer>
      )}
    </div>
  )
}
