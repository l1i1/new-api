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

import { DEFAULT_CONFIG, DEFAULT_PARAMETER_ENABLED } from '../constants'
import {
  saveConfig,
  saveParameterEnabled,
  saveSessions,
  loadSessions,
  loadActiveSessionId,
  saveActiveSessionId,
  applyMessageStateUpdate,
  getInitialParameterEnabled,
  getInitialPlaygroundConfig,
  type MessageStateUpdater,
} from '../lib'
import type {
  Message,
  PlaygroundConfig,
  ParameterEnabled,
  ModelOption,
  GroupOption,
  PlaygroundSession,
} from '../types'

const SESSION_SAVE_DEBOUNCE_MS = 500
const SESSION_TITLE_MAX_CHARS = 24

function createEmptySession(): PlaygroundSession {
  const now = Date.now()
  return {
    id: `session-${now}-${Math.random().toString(36).slice(2, 8)}`,
    title: '',
    createdAt: now,
    updatedAt: now,
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

function sortSessionsByRecency(
  sessions: PlaygroundSession[]
): PlaygroundSession[] {
  return [...sessions].sort((a, b) => b.updatedAt - a.updatedAt)
}

/**
 * Main state management hook for playground. Conversations are stored as
 * multiple sessions, all persisted in browser localStorage.
 */
export function usePlaygroundState() {
  // Load initial state from localStorage
  const [config, setConfig] = useState<PlaygroundConfig>(
    getInitialPlaygroundConfig
  )

  const [parameterEnabled, setParameterEnabled] = useState<ParameterEnabled>(
    getInitialParameterEnabled
  )

  const [sessions, setSessions] = useState<PlaygroundSession[]>([])
  const [activeSessionId, setActiveSessionId] = useState<string | null>(null)
  const [isLoadingSessions, setIsLoadingSessions] = useState(true)
  const sessionsSaveTimerRef = useRef<number | null>(null)
  const latestSessionsRef = useRef<PlaygroundSession[]>(sessions)
  const hasLoadedSessionsRef = useRef(false)

  const [models, setModels] = useState<ModelOption[]>([])
  const [groups, setGroups] = useState<GroupOption[]>([])

  const persistSessions = useCallback(
    (sessionsToSave: PlaygroundSession[]) => {
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
    },
    []
  )

  useEffect(() => {
    let cancelled = false

    window.setTimeout(() => {
      const loaded = loadSessions() ?? []
      const initialSessions =
        loaded.length > 0 ? sortSessionsByRecency(loaded) : [createEmptySession()]
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

  // Update config with automatic save
  const updateConfig = useCallback(
    <K extends keyof PlaygroundConfig>(key: K, value: PlaygroundConfig[K]) => {
      setConfig((prev) => {
        const updated = { ...prev, [key]: value }
        saveConfig(updated)
        return updated
      })
    },
    []
  )

  // Update parameter enabled with automatic save
  const updateParameterEnabled = useCallback(
    (key: keyof ParameterEnabled, value: boolean) => {
      setParameterEnabled((prev) => {
        const updated = { ...prev, [key]: value }
        saveParameterEnabled(updated)
        return updated
      })
    },
    []
  )

  // Update messages of the active session with automatic save. When no
  // session exists yet, the first message creates one implicitly.
  const updateMessages = useCallback(
    (updater: MessageStateUpdater) => {
      setSessions((prev) => {
        const currentId =
          activeSessionId ??
          prev[0]?.id ??
          null
        const base =
          currentId && prev.some((session) => session.id === currentId)
            ? prev
            : [...prev, createEmptySession()]
        const targetId =
          currentId && base.some((session) => session.id === currentId)
            ? currentId
            : (base.at(-1)?.id ?? '')

        const next = base.map((session) => {
          if (session.id !== targetId) return session
          const messages = applyMessageStateUpdate(session.messages, updater)
          return {
            ...session,
            messages,
            updatedAt: Date.now(),
            title: session.title || deriveSessionTitle(messages),
          }
        })

        if (targetId !== activeSessionId) {
          setActiveSessionId(targetId)
          saveActiveSessionId(targetId)
        }

        const sorted = sortSessionsByRecency(next)
        persistSessions(sorted)
        return sorted
      })
    },
    [activeSessionId, persistSessions]
  )

  const activeSession =
    sessions.find((session) => session.id === activeSessionId) ??
    sessions[0] ??
    null
  const messages = activeSession?.messages ?? []

  const createSession = useCallback(() => {
    const session = createEmptySession()
    setSessions((prev) => {
      const next = [session, ...prev]
      persistSessions(next)
      return next
    })
    setActiveSessionId(session.id)
    saveActiveSessionId(session.id)
    return session.id
  }, [persistSessions])

  const selectSession = useCallback(
    (id: string) => {
      setActiveSessionId(id)
      saveActiveSessionId(id)
    },
    []
  )

  const deleteSession = useCallback(
    (id: string) => {
      setSessions((prev) => {
        const remaining = prev.filter((session) => session.id !== id)
        const next =
          remaining.length > 0
            ? sortSessionsByRecency(remaining)
            : [createEmptySession()]

        if (id === activeSessionId || !next.some((s) => s.id === activeSessionId)) {
          setActiveSessionId(next[0].id)
          saveActiveSessionId(next[0].id)
        }

        persistSessions(next)
        return next
      })
    },
    [activeSessionId, persistSessions]
  )

  // Clear all messages of the active session
  const clearMessages = useCallback(() => {
    updateMessages([])
  }, [updateMessages])

  // Reset config to defaults
  const resetConfig = useCallback(() => {
    setConfig(DEFAULT_CONFIG)
    setParameterEnabled(DEFAULT_PARAMETER_ENABLED)
    saveConfig(DEFAULT_CONFIG)
    saveParameterEnabled(DEFAULT_PARAMETER_ENABLED)
  }, [])

  return {
    // State
    config,
    parameterEnabled,
    sessions,
    activeSessionId,
    messages,
    isLoadingSessions,
    models,
    groups,

    // Setters
    setModels,
    setGroups,

    // Actions
    updateConfig,
    updateParameterEnabled,
    updateMessages,
    createSession,
    selectSession,
    deleteSession,
    clearMessages,
    resetConfig,
  }
}
