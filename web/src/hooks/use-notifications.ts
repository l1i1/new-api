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
import { useCallback, useEffect, useMemo, useState } from 'react'

import { useStatus } from '@/hooks/use-status'
import { getNotice } from '@/lib/api'
import { useNotificationStore } from '@/stores/notification-store'

export const NOTIFICATION_REFRESH_INTERVAL_MS = 5 * 60 * 1000

export function getNotificationRefreshInterval(
  enabled: boolean
): number | false {
  return enabled ? NOTIFICATION_REFRESH_INTERVAL_MS : false
}

function hashString(input: string): string {
  let hash = 0
  if (!input) return '0'

  for (let i = 0; i < input.length; i += 1) {
    const chr = input.charCodeAt(i)
    hash = (hash << 5) - hash + chr
    hash |= 0
  }

  return hash.toString(36)
}

/** Generate a stable key for an announcement revision. */
export function getAnnouncementKey(item: Record<string, unknown>): string {
  if (!item) return ''

  const fingerprint = JSON.stringify({
    publishDate: (item?.publishDate as string) || '',
    content: ((item?.content as string) || '').trim(),
    extra: ((item?.extra as string) || '').trim(),
    type: (item?.type as string) || '',
    title: ((item?.title as string) || '').trim(),
    link: ((item?.link as string) || '').trim(),
  })
  const revision = hashString(fingerprint)

  if (item.id !== undefined && item.id !== null) {
    return `id:${item.id}:${revision}`
  }

  return `hash:${revision}`
}

/**
 * Hook to manage notifications (Notice + Announcements)
 * Provides unread counts and read status management
 */
export function getNotificationAutoOpenOptions(pathname: string): {
  autoOpenNotice: boolean
  autoOpenPopover: boolean
} {
  const normalizedPathname =
    pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname
  const autoOpenNotice = normalizedPathname === '/pricing'

  return {
    autoOpenNotice,
    autoOpenPopover: normalizedPathname !== '/' && !autoOpenNotice,
  }
}

export function useNotifications(
  options: {
    autoOpenNotice?: boolean
    autoOpenPopover?: boolean
    pollAnnouncements?: boolean
  } = {}
) {
  const [popoverOpen, setPopoverOpen] = useState(false)
  const [siteNoticeOpen, setSiteNoticeOpen] = useState(false)
  const [autoOpenedPopover, setAutoOpenedPopover] = useState(false)

  // Fetch Notice from API
  const {
    data: noticeResponse,
    isFetching: noticeFetching,
    isLoading: noticeLoading,
    refetch: refetchNotice,
  } = useQuery({
    queryKey: ['notice'],
    queryFn: getNotice,
    staleTime: 1000 * 60 * 5, // 5 minutes
    refetchInterval:
      options.autoOpenNotice || options.autoOpenPopover ? 1000 * 60 * 5 : false,
    refetchOnMount:
      options.autoOpenNotice || options.autoOpenPopover ? 'always' : true,
  })

  // Fetch Announcements from status
  const { status, loading: statusLoading } = useStatus({
    refetchInterval: getNotificationRefreshInterval(
      options.pollAnnouncements ?? false
    ),
  })
  const announcementsEnabled = status?.announcements_enabled ?? false
  const announcements = useMemo<Record<string, unknown>[]>(() => {
    if (!announcementsEnabled) return []

    return ((status?.announcements || []) as Record<string, unknown>[]).slice(
      0,
      20
    )
  }, [announcementsEnabled, status?.announcements])

  // Notification store
  const {
    lastReadNotice,
    lastAutoOpenedPricingNotice,
    pendingAutoOpenKey,
    markNoticeRead,
    markPricingNoticeAutoOpened,
    markAnnouncementsRead,
    setPendingAutoOpenKey,
    lastObservedNotificationRevision,
    setLastObservedNotificationRevision,
    pendingSiteNoticeKey,
    setPendingSiteNoticeKey,
    readAnnouncementKeys,
    isNoticeClosed,
    setClosedUntilDate,
  } = useNotificationStore()

  // Extract notice content
  const noticeContent = noticeResponse?.success
    ? (noticeResponse.data || '').trim()
    : ''

  const notificationRevision = useMemo(
    () =>
      JSON.stringify({
        notice: noticeContent,
        announcements: announcements.map((item) => ({
          key: getAnnouncementKey(item),
          content: ((item.content as string) || '').trim(),
          extra: ((item.extra as string) || '').trim(),
          publishDate: (item.publishDate as string) || '',
          type: (item.type as string) || '',
        })),
      }),
    [announcements, noticeContent]
  )

  // Calculate unread counts
  const unreadCounts = useMemo(() => {
    const noticeUnread =
      noticeContent && noticeContent !== lastReadNotice ? 1 : 0

    const announcementsUnread = announcements.filter(
      (item: Record<string, unknown>) => {
        const key = getAnnouncementKey(item)
        return !readAnnouncementKeys.includes(key)
      }
    ).length

    return {
      notice: noticeUnread,
      announcements: announcementsUnread,
      total: noticeUnread + announcementsUnread,
    }
  }, [noticeContent, lastReadNotice, announcements, readAnnouncementKeys])

  const displayAnnouncements = useMemo(
    () =>
      announcements.map((item) => ({
        ...item,
        unread: !readAnnouncementKeys.includes(getAnnouncementKey(item)),
      })),
    [announcements, readAnnouncementKeys]
  )

  const markAnnouncementsAsRead = useCallback(() => {
    if (announcements.length > 0) {
      const allKeys = announcements.map((item: Record<string, unknown>) =>
        getAnnouncementKey(item)
      )
      markAnnouncementsRead(allKeys)
    }
  }, [announcements, markAnnouncementsRead])

  const markAllAsRead = useCallback(() => {
    if (noticeContent) {
      markNoticeRead(noticeContent)
    }
    markAnnouncementsAsRead()
  }, [markAnnouncementsAsRead, markNoticeRead, noticeContent])

  // Handle popover open
  const handleOpenPopover = useCallback(
    (markAsRead = true) => {
      if (markAsRead) {
        markAllAsRead()
        setPendingAutoOpenKey(null)
        setAutoOpenedPopover(false)
      }

      setPopoverOpen(true)
    },
    [markAllAsRead, setPendingAutoOpenKey]
  )

  useEffect(() => {
    if (!options.autoOpenNotice || noticeFetching || !noticeContent) return

    const hasPendingRevision = pendingSiteNoticeKey === noticeContent
    const isNewNotice = noticeContent !== lastAutoOpenedPricingNotice
    if (!hasPendingRevision && !isNewNotice) return

    if (isNewNotice) {
      markPricingNoticeAutoOpened(noticeContent)
      markNoticeRead(noticeContent)
    }
    if (!hasPendingRevision) {
      setPendingSiteNoticeKey(noticeContent)
    }
    setSiteNoticeOpen(true)
  }, [
    lastAutoOpenedPricingNotice,
    markNoticeRead,
    markPricingNoticeAutoOpened,
    noticeContent,
    noticeFetching,
    options.autoOpenNotice,
    pendingSiteNoticeKey,
    setPendingSiteNoticeKey,
  ])

  useEffect(() => {
    if (
      !options.autoOpenPopover ||
      noticeFetching ||
      statusLoading ||
      isNoticeClosed()
    ) {
      return
    }

    if (!lastObservedNotificationRevision) {
      setLastObservedNotificationRevision(notificationRevision)
      if (unreadCounts.total > 0) {
        setPendingAutoOpenKey(notificationRevision)
        setAutoOpenedPopover(true)
        setPopoverOpen(true)
      } else {
        setPendingAutoOpenKey(null)
      }
      return
    }

    const hasPendingRevision = pendingAutoOpenKey === notificationRevision
    if (lastObservedNotificationRevision === notificationRevision) {
      if (hasPendingRevision && !popoverOpen) {
        setAutoOpenedPopover(true)
        setPopoverOpen(true)
      }
      return
    }

    setLastObservedNotificationRevision(notificationRevision)
    if (unreadCounts.total === 0) {
      setPendingAutoOpenKey(null)
      return
    }
    setPendingAutoOpenKey(notificationRevision)
    setAutoOpenedPopover(true)
    setPopoverOpen(true)
  }, [
    lastObservedNotificationRevision,
    noticeFetching,
    notificationRevision,
    options.autoOpenPopover,
    pendingAutoOpenKey,
    popoverOpen,
    setLastObservedNotificationRevision,
    setPendingAutoOpenKey,
    statusLoading,
    unreadCounts.total,
    isNoticeClosed,
  ])

  const handlePopoverOpenChange = (open: boolean) => {
    if (open) {
      handleOpenPopover()
      return
    }

    setPopoverOpen(false)
    if (autoOpenedPopover) {
      markAllAsRead()
      setAutoOpenedPopover(false)
    }
    if (pendingAutoOpenKey) {
      setPendingAutoOpenKey(null)
    }
  }

  const closeToday = useCallback(() => {
    setClosedUntilDate(new Date().toDateString())
    setPopoverOpen(false)
    setAutoOpenedPopover(false)
    setPendingAutoOpenKey(null)
  }, [setClosedUntilDate, setPendingAutoOpenKey])

  const handleSiteNoticeOpenChange = (open: boolean) => {
    setSiteNoticeOpen(open)
    if (!open) {
      if (noticeContent) {
        markNoticeRead(noticeContent)
      }
      setPendingSiteNoticeKey(null)
    }
  }

  return {
    // Data
    notice: noticeContent,
    announcements: displayAnnouncements,
    loading: noticeLoading || statusLoading,

    // Unread counts
    unreadCount: unreadCounts.total,
    unreadNoticeCount: unreadCounts.notice,
    unreadAnnouncementsCount: unreadCounts.announcements,

    // Popover state
    popoverOpen,
    setPopoverOpen: handlePopoverOpenChange,
    siteNoticeOpen,
    setSiteNoticeOpen: handleSiteNoticeOpenChange,

    // Actions
    openPopover: handleOpenPopover,
    closePopover: () => handlePopoverOpenChange(false),
    closeToday,
    refetchNotice,
  }
}
