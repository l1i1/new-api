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
import { Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { toIntlLocale } from '@/i18n/languages'
import { cn } from '@/lib/utils'

import type { PlaygroundSession } from '../../types'

type PlaygroundSessionListProps = {
  sessions: PlaygroundSession[]
  activeSessionId: string | null
  onCreateSession: () => void
  onSelectSession: (id: string) => void
  onDeleteSession: (id: string) => void
  /** Called after a session is picked so mobile drawers can close. */
  onNavigate?: () => void
  className?: string
}

function formatSessionTime(timestamp: number, locale?: string): string {
  const date = new Date(timestamp)
  const now = new Date()
  const sameDay =
    date.getFullYear() === now.getFullYear() &&
    date.getMonth() === now.getMonth() &&
    date.getDate() === now.getDate()
  if (sameDay) {
    return date.toLocaleTimeString(locale, {
      hour: '2-digit',
      minute: '2-digit',
    })
  }
  return date.toLocaleDateString(locale, { month: '2-digit', day: '2-digit' })
}

export function PlaygroundSessionList(props: PlaygroundSessionListProps) {
  const { i18n, t } = useTranslation()
  const [pendingDeleteId, setPendingDeleteId] = useState<string | null>(null)

  const handleConfirmDelete = () => {
    if (pendingDeleteId) {
      props.onDeleteSession(pendingDeleteId)
    }
    setPendingDeleteId(null)
  }

  return (
    <div className={cn('flex h-full min-h-0 flex-col', props.className)}>
      <div className='border-border flex items-center justify-between border-b px-3 py-2'>
        <span className='text-sm font-semibold'>{t('Chats')}</span>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          className='h-7 gap-1 px-2 text-xs'
          onClick={props.onCreateSession}
        >
          <Plus className='size-3.5' aria-hidden='true' />
          {t('New chat')}
        </Button>
      </div>

      <ScrollArea className='min-h-0 flex-1'>
        <ul className='divide-border divide-y'>
          {props.sessions.map((session) => {
            const isActive = session.id === props.activeSessionId
            return (
              <li key={session.id}>
                <div
                  role='button'
                  tabIndex={0}
                  onClick={() => {
                    props.onSelectSession(session.id)
                    props.onNavigate?.()
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter' || event.key === ' ') {
                      event.preventDefault()
                      props.onSelectSession(session.id)
                      props.onNavigate?.()
                    }
                  }}
                  className={cn(
                    'group focus-visible:ring-ring flex w-full cursor-pointer items-center gap-2 px-3 py-2.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset',
                    isActive ? 'bg-muted/70' : 'hover:bg-muted/40'
                  )}
                >
                  <span className='min-w-0 flex-1'>
                    <span
                      className={cn(
                        'block truncate text-sm',
                        isActive
                          ? 'text-foreground font-medium'
                          : 'text-muted-foreground group-hover:text-foreground'
                      )}
                    >
                      {session.title || t('New chat')}
                    </span>
                    <span className='text-muted-foreground/60 mt-0.5 block text-xs tabular-nums'>
                      {formatSessionTime(
                        session.updatedAt,
                        toIntlLocale(i18n.resolvedLanguage || i18n.language)
                      )}
                    </span>
                  </span>
                  <button
                    type='button'
                    aria-label={t('Delete chat')}
                    className='text-muted-foreground/50 hover:text-destructive focus-visible:ring-ring shrink-0 rounded-none opacity-0 transition-opacity outline-none group-hover:opacity-100 focus-visible:opacity-100 focus-visible:ring-2'
                    onClick={(event) => {
                      event.stopPropagation()
                      setPendingDeleteId(session.id)
                    }}
                  >
                    <Trash2 className='size-3.5' aria-hidden='true' />
                  </button>
                </div>
              </li>
            )
          })}
        </ul>
      </ScrollArea>

      <ConfirmDialog
        destructive
        title={t('Delete chat?')}
        desc={t('This conversation will be permanently deleted.')}
        confirmText={t('Delete')}
        open={pendingDeleteId !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDeleteId(null)
        }}
        handleConfirm={handleConfirmDelete}
      />
    </div>
  )
}
