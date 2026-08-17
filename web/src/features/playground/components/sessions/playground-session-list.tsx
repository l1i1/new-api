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
import {
  Check,
  GripVertical,
  Pencil,
  Plus,
  Search,
  Trash2,
  X,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { toIntlLocale } from '@/i18n/languages'
import { cn } from '@/lib/utils'

import type { SessionReorderPlacement } from '../../hooks/use-playground-state'
import type { PlaygroundSession } from '../../types'

type PlaygroundSessionListProps = {
  sessions: PlaygroundSession[]
  activeSessionId: string | null
  onCreateSession: () => void
  onSelectSession: (id: string) => void
  onDeleteSession: (id: string) => void
  onRenameSession: (id: string, title: string) => void
  onReorderSessions: (
    sourceId: string,
    targetId: string,
    placement?: SessionReorderPlacement
  ) => void
  /** Called after a session is picked so mobile drawers can close. */
  onNavigate?: () => void
  disabled?: boolean
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
  const [searchText, setSearchText] = useState('')
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingTitle, setEditingTitle] = useState('')
  const [draggedId, setDraggedId] = useState<string | null>(null)

  const visibleSessions = useMemo(() => {
    const query = searchText.trim().toLocaleLowerCase()
    if (!query) return props.sessions
    return props.sessions.filter((session) =>
      (session.title || t('New chat')).toLocaleLowerCase().includes(query)
    )
  }, [props.sessions, searchText, t])

  const startEditing = (session: PlaygroundSession) => {
    setEditingId(session.id)
    setEditingTitle(session.title)
  }

  const finishEditing = () => {
    if (!editingId) return
    props.onRenameSession(editingId, editingTitle)
    setEditingId(null)
    setEditingTitle('')
  }

  const handleConfirmDelete = () => {
    if (pendingDeleteId) {
      props.onDeleteSession(pendingDeleteId)
    }
    setPendingDeleteId(null)
  }

  const reorderWithKeyboard = (sessionId: string, direction: 'up' | 'down') => {
    const sourceIndex = props.sessions.findIndex(
      (session) => session.id === sessionId
    )
    const targetIndex = sourceIndex + (direction === 'up' ? -1 : 1)
    const target = props.sessions[targetIndex]
    if (sourceIndex < 0 || !target) return
    props.onReorderSessions(
      sessionId,
      target.id,
      direction === 'up' ? 'before' : 'after'
    )
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
          disabled={props.disabled}
          onClick={props.onCreateSession}
        >
          <Plus className='size-3.5' aria-hidden='true' />
          {t('New chat')}
        </Button>
      </div>

      <div className='border-border border-b px-3 py-2'>
        <div className='relative'>
          <Search
            aria-hidden='true'
            className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2'
          />
          <Input
            aria-label={t('Search chats')}
            className='h-8 rounded-none pr-8 pl-8 text-xs'
            onChange={(event) => setSearchText(event.target.value)}
            placeholder={t('Search chats')}
            type='search'
            value={searchText}
          />
          {searchText && (
            <button
              aria-label={t('Clear search')}
              className='text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2'
              onClick={() => setSearchText('')}
              type='button'
            >
              <X className='size-3.5' />
            </button>
          )}
        </div>
      </div>

      <ScrollArea className='min-h-0 flex-1'>
        <ul className='divide-border divide-y'>
          {visibleSessions.map((session) => {
            const isActive = session.id === props.activeSessionId
            const isEditing = editingId === session.id
            return (
              <li
                draggable={!isEditing && !props.disabled}
                key={session.id}
                onDragEnd={() => setDraggedId(null)}
                onDragOver={(event) => {
                  event.preventDefault()
                  event.dataTransfer.dropEffect = 'move'
                }}
                onDragStart={(event) => {
                  event.dataTransfer.effectAllowed = 'move'
                  event.dataTransfer.setData('text/plain', session.id)
                  setDraggedId(session.id)
                }}
                onDrop={(event) => {
                  event.preventDefault()
                  const sourceId =
                    draggedId || event.dataTransfer.getData('text/plain')
                  if (!sourceId) {
                    setDraggedId(null)
                    return
                  }

                  const rect = event.currentTarget.getBoundingClientRect()
                  const placement =
                    event.clientY < rect.top + rect.height / 2
                      ? 'before'
                      : 'after'
                  props.onReorderSessions(sourceId, session.id, placement)
                  setDraggedId(null)
                }}
              >
                <div
                  role='button'
                  tabIndex={props.disabled ? -1 : 0}
                  onClick={() => {
                    if (isEditing || props.disabled) return
                    props.onSelectSession(session.id)
                    props.onNavigate?.()
                  }}
                  onKeyDown={(event) => {
                    if (isEditing || props.disabled) return
                    if (event.key === 'Enter' || event.key === ' ') {
                      event.preventDefault()
                      props.onSelectSession(session.id)
                      props.onNavigate?.()
                    }
                  }}
                  className={cn(
                    'group focus-visible:ring-ring flex w-full cursor-pointer items-center gap-1.5 px-2 py-2.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset',
                    isActive ? 'bg-muted/70' : 'hover:bg-muted/40',
                    draggedId === session.id && 'opacity-50'
                  )}
                >
                  <button
                    aria-keyshortcuts='ArrowUp ArrowDown'
                    aria-label={`${t('Reorder chats')}: ${session.title || t('New chat')}`}
                    className='text-muted-foreground/50 hover:text-foreground focus-visible:ring-ring shrink-0 cursor-grab rounded-none p-0.5 outline-none focus-visible:ring-2'
                    disabled={props.disabled}
                    onClick={(event) => event.stopPropagation()}
                    onKeyDown={(event) => {
                      if (
                        event.key !== 'ArrowUp' &&
                        event.key !== 'ArrowDown'
                      ) {
                        return
                      }
                      event.preventDefault()
                      event.stopPropagation()
                      reorderWithKeyboard(
                        session.id,
                        event.key === 'ArrowUp' ? 'up' : 'down'
                      )
                    }}
                    type='button'
                  >
                    <GripVertical className='size-3.5' aria-hidden='true' />
                  </button>
                  <span className='min-w-0 flex-1'>
                    {isEditing ? (
                      <Input
                        aria-label={t('Chat title')}
                        autoFocus
                        className='h-7 rounded-none px-1.5 text-sm'
                        onChange={(event) =>
                          setEditingTitle(event.target.value)
                        }
                        onClick={(event) => event.stopPropagation()}
                        onKeyDown={(event) => {
                          if (event.key === 'Enter') {
                            event.preventDefault()
                            finishEditing()
                          }
                          if (event.key === 'Escape') {
                            event.preventDefault()
                            setEditingId(null)
                            setEditingTitle('')
                          }
                        }}
                        onBlur={finishEditing}
                        value={editingTitle}
                      />
                    ) : (
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
                    )}
                    {!isEditing && (
                      <span className='text-muted-foreground/60 mt-0.5 block text-xs tabular-nums'>
                        {formatSessionTime(
                          session.updatedAt,
                          toIntlLocale(i18n.resolvedLanguage || i18n.language)
                        )}
                      </span>
                    )}
                  </span>
                  {isEditing ? (
                    <button
                      aria-label={t('Save')}
                      className='text-muted-foreground hover:text-foreground shrink-0 rounded-none p-1'
                      onClick={(event) => {
                        event.stopPropagation()
                        finishEditing()
                      }}
                      type='button'
                    >
                      <Check className='size-3.5' />
                    </button>
                  ) : (
                    <>
                      <button
                        aria-label={t('Rename chat')}
                        className='text-muted-foreground/50 hover:text-foreground focus-visible:ring-ring shrink-0 rounded-none p-1 opacity-0 outline-none group-hover:opacity-100 focus-visible:opacity-100 focus-visible:ring-2'
                        disabled={props.disabled}
                        onClick={(event) => {
                          event.stopPropagation()
                          startEditing(session)
                        }}
                        type='button'
                      >
                        <Pencil className='size-3.5' />
                      </button>
                      <button
                        aria-label={t('Delete chat')}
                        className='text-muted-foreground/50 hover:text-destructive focus-visible:ring-ring shrink-0 rounded-none p-1 opacity-0 outline-none group-hover:opacity-100 focus-visible:opacity-100 focus-visible:ring-2'
                        disabled={props.disabled}
                        onClick={(event) => {
                          event.stopPropagation()
                          setPendingDeleteId(session.id)
                        }}
                        type='button'
                      >
                        <Trash2 className='size-3.5' aria-hidden='true' />
                      </button>
                    </>
                  )}
                </div>
              </li>
            )
          })}
        </ul>
        {visibleSessions.length === 0 && (
          <p className='text-muted-foreground px-3 py-6 text-center text-xs'>
            {t('No chats found')}
          </p>
        )}
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
