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
import { useCallback, useEffect, useRef, useState } from 'react'

import {
  DEFAULT_CONFIG,
  DEFAULT_PARAMETER_ENABLED,
  PLAYGROUND_MODES,
  type PlaygroundMode,
} from '../constants'
import {
  applyMessageStateUpdate,
  getInitialParameterEnabled,
  getInitialPlaygroundConfig,
  loadActiveSessionId,
  loadSessions,
  saveActiveSessionId,
  saveConfig,
  saveParameterEnabled,
  saveSessions,
  type MessageStateUpdater,
} from '../lib'
import type {
  GroupOption,
  Message,
  ModelOption,
  ParameterEnabled,
  PlaygroundConfig,
  PlaygroundSession,
} from '../types'

const SESSION_SAVE_DEBOUNCE_MS = 500
const SESSION_TITLE_MAX_CHARS = 24

export type SessionReorderPlacement = 'before' | 'after'

/** Move a session to an explicit position relative to the target row. */
export function reorderSessionList(
  sessions: PlaygroundSession[],
  sourceId: string,
  targetId: string,
  placement: SessionReorderPlacement = 'before'
): PlaygroundSession[] {
  const sourceIndex = sessions.findIndex((session) => session.id === sourceId)
  const targetIndex = sessions.findIndex((session) => session.id === targetId)

  if (sourceIndex === -1 || targetIndex === -1 || sourceIndex === targetIndex) {
    return sessions
  }

  const next = [...sessions]
  const [moved] = next.splice(sourceIndex, 1)
  if (!moved) return sessions

  const targetIndexAfterRemoval = next.findIndex(
    (session) => session.id === targetId
  )
  const insertionIndex =
    targetIndexAfterRemoval + (placement === 'after' ? 1 : 0)
  if (insertionIndex === sourceIndex) return sessions

  next.splice(insertionIndex, 0, moved)
  return next
}

function createEmptySession(
  config: PlaygroundConfig = DEFAULT_CONFIG,
  parameterEnabled: ParameterEnabled = DEFAULT_PARAMETER_ENABLED,
  mode: PlaygroundMode = PLAYGROUND_MODES.CHAT
): PlaygroundSession {
  const now = Date.now()
  return {
    id: `session-${now}-${Math.random().toString(36).slice(2, 8)}`,
    title: '',
    createdAt: now,
    updatedAt: now,
    mode,
    config: { ...config },
    parameterEnabled: { ...parameterEnabled },
    messages: [],
  }
}

function deriveSessionTitle(messages: Message[]): string {
  const firstUserMessage = messages.find((message) => message.from === 'user')
  const text = firstUserMessage?.versions[0]?.content.trim() ?? ''
  if (!text) return ''
  return text.length > SESSION_TITLE_MAX_CHARS
    ? `${text.slice(0, SESSION_TITLE_MAX_CHARS)}…`
    : text
}

/**
 * Main state management hook for playground. Conversations and their
 * controls are stored together so switching sessions restores its context.
 */
export function usePlaygroundState() {
  const initialConfigRef = useRef<PlaygroundConfig | null>(null)
  const initialParameterEnabledRef = useRef<ParameterEnabled | null>(null)
  if (initialConfigRef.current === null) {
    initialConfigRef.current = getInitialPlaygroundConfig()
  }
  if (initialParameterEnabledRef.current === null) {
    initialParameterEnabledRef.current = getInitialParameterEnabled()
  }

  const [sessions, setSessions] = useState<PlaygroundSession[]>([])
  const [activeSessionId, setActiveSessionId] = useState<string | null>(null)
  const [isLoadingSessions, setIsLoadingSessions] = useState(true)
  const sessionsSaveTimerRef = useRef<number | null>(null)
  const latestSessionsRef = useRef<PlaygroundSession[]>(sessions)
  const hasLoadedSessionsRef = useRef(false)

  const [models, setModels] = useState<ModelOption[]>([])
  const [groups, setGroups] = useState<GroupOption[]>([])

  const persistSessions = useCallback((sessionsToSave: PlaygroundSession[]) => {
    latestSessionsRef.current = sessionsToSave

    if (!hasLoadedSessionsRef.current) {
      return
    }

    if (sessionsSaveTimerRef.current !== null) {
      window.clearTimeout(sessionsSaveTimerRef.current)
    }

    sessionsSaveTimerRef.current = window.setTimeout(() => {
      sessionsSaveTimerRef.current = null
      saveSessions(latestSessionsRef.current)
    }, SESSION_SAVE_DEBOUNCE_MS)
  }, [])

  useEffect(() => {
    let cancelled = false

    window.setTimeout(() => {
      const loaded = loadSessions() ?? []
      const initialSessions =
        loaded.length > 0
          ? loaded
          : [
              createEmptySession(
                initialConfigRef.current ?? DEFAULT_CONFIG,
                initialParameterEnabledRef.current ?? DEFAULT_PARAMETER_ENABLED
              ),
            ]
      if (cancelled) {
        return
      }

      const savedActiveId = loadActiveSessionId()
      const initialActiveId =
        savedActiveId &&
        initialSessions.some((session) => session.id === savedActiveId)
          ? savedActiveId
          : initialSessions[0].id

      latestSessionsRef.current = initialSessions
      hasLoadedSessionsRef.current = true
      setSessions(initialSessions)
      setActiveSessionId(initialActiveId)
      setIsLoadingSessions(false)
    }, 0)

    return () => {
      cancelled = true
    }
  }, [])

  useEffect(
    () => () => {
      if (sessionsSaveTimerRef.current !== null) {
        window.clearTimeout(sessionsSaveTimerRef.current)
        saveSessions(latestSessionsRef.current)
      }
    },
    []
  )

  const updateSession = useCallback(
    (
      sessionId: string,
      updater: (session: PlaygroundSession) => PlaygroundSession
    ) => {
      setSessions((previousSessions) => {
        const index = previousSessions.findIndex(
          (session) => session.id === sessionId
        )
        if (index === -1) return previousSessions

        const next = [...previousSessions]
        next[index] = updater(previousSessions[index])
        persistSessions(next)
        return next
      })
    },
    [persistSessions]
  )

  const updateSessionMessages = useCallback(
    (sessionId: string, updater: MessageStateUpdater) => {
      updateSession(sessionId, (session) => {
        const messages = applyMessageStateUpdate(session.messages, updater)
        return {
          ...session,
          messages,
          updatedAt: Date.now(),
          title: session.title || deriveSessionTitle(messages),
        }
      })
    },
    [updateSession]
  )

  const activeSession =
    sessions.find((session) => session.id === activeSessionId) ??
    sessions[0] ??
    null
  const messages = activeSession?.messages ?? []
  const config =
    activeSession?.config ?? initialConfigRef.current ?? DEFAULT_CONFIG
  const parameterEnabled =
    activeSession?.parameterEnabled ??
    initialParameterEnabledRef.current ??
    DEFAULT_PARAMETER_ENABLED
  const mode = activeSession?.mode ?? PLAYGROUND_MODES.CHAT

  const updateMessages = useCallback(
    (updater: MessageStateUpdater) => {
      if (!activeSession?.id) return
      updateSessionMessages(activeSession.id, updater)
    },
    [activeSession?.id, updateSessionMessages]
  )

  const updateConfig = useCallback(
    <K extends keyof PlaygroundConfig>(key: K, value: PlaygroundConfig[K]) => {
      if (!activeSession?.id) return
      updateSession(activeSession.id, (session) => {
        const updatedConfig = { ...session.config, [key]: value }
        saveConfig(updatedConfig)
        return { ...session, config: updatedConfig }
      })
    },
    [activeSession?.id, updateSession]
  )

  const updateParameterEnabled = useCallback(
    (key: keyof ParameterEnabled, value: boolean) => {
      if (!activeSession?.id) return
      updateSession(activeSession.id, (session) => {
        const updatedParameterEnabled = {
          ...session.parameterEnabled,
          [key]: value,
        }
        saveParameterEnabled(updatedParameterEnabled)
        return { ...session, parameterEnabled: updatedParameterEnabled }
      })
    },
    [activeSession?.id, updateSession]
  )

  const updateMode = useCallback(
    (nextMode: PlaygroundMode) => {
      if (!activeSession?.id) return
      updateSession(activeSession.id, (session) => ({
        ...session,
        mode: nextMode,
      }))
    },
    [activeSession?.id, updateSession]
  )

  const createSession = useCallback(() => {
    const session = createEmptySession(config, parameterEnabled, mode)
    setSessions((previousSessions) => {
      const next = [session, ...previousSessions]
      persistSessions(next)
      return next
    })
    setActiveSessionId(session.id)
    saveActiveSessionId(session.id)
    return session.id
  }, [config, mode, parameterEnabled, persistSessions])

  const selectSession = useCallback((id: string) => {
    setActiveSessionId(id)
    saveActiveSessionId(id)
  }, [])

  const deleteSession = useCallback(
    (id: string) => {
      setSessions((previousSessions) => {
        const remaining = previousSessions.filter(
          (session) => session.id !== id
        )
        const next =
          remaining.length > 0
            ? remaining
            : [
                createEmptySession(
                  initialConfigRef.current ?? DEFAULT_CONFIG,
                  initialParameterEnabledRef.current ??
                    DEFAULT_PARAMETER_ENABLED
                ),
              ]

        if (
          id === activeSessionId ||
          !next.some((s) => s.id === activeSessionId)
        ) {
          setActiveSessionId(next[0].id)
          saveActiveSessionId(next[0].id)
        }

        persistSessions(next)
        return next
      })
    },
    [activeSessionId, persistSessions]
  )

  const renameSession = useCallback(
    (id: string, title: string) => {
      updateSession(id, (session) => ({
        ...session,
        title: title.trim().slice(0, 80),
      }))
    },
    [updateSession]
  )

  const reorderSessions = useCallback(
    (
      sourceId: string,
      targetId: string,
      placement: SessionReorderPlacement = 'before'
    ) => {
      setSessions((previousSessions) => {
        const next = reorderSessionList(
          previousSessions,
          sourceId,
          targetId,
          placement
        )
        if (next === previousSessions) {
          return previousSessions
        }

        persistSessions(next)
        return next
      })
    },
    [persistSessions]
  )

  const clearMessages = useCallback(() => {
    updateMessages([])
  }, [updateMessages])

  const resetConfig = useCallback(() => {
    if (!activeSession?.id) return
    updateSession(activeSession.id, (session) => ({
      ...session,
      config: { ...DEFAULT_CONFIG },
      parameterEnabled: { ...DEFAULT_PARAMETER_ENABLED },
    }))
    saveConfig(DEFAULT_CONFIG)
    saveParameterEnabled(DEFAULT_PARAMETER_ENABLED)
  }, [activeSession?.id, updateSession])

  return {
    config,
    parameterEnabled,
    mode,
    sessions,
    activeSessionId,
    messages,
    isLoadingSessions,
    models,
    groups,
    setModels,
    setGroups,
    updateConfig,
    updateParameterEnabled,
    updateMode,
    updateMessages,
    updateSessionMessages,
    createSession,
    selectSession,
    deleteSession,
    renameSession,
    reorderSessions,
    clearMessages,
    resetConfig,
  }
}
