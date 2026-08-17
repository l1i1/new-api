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
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { sendChatCompletion } from '../api'
import { ERROR_MESSAGES } from '../constants'
import {
  applyChatCompletionResponse,
  applyStreamingChunk,
  buildChatCompletionPayload,
  completeAssistantMessage,
  hasChatCompletionChoice,
  isAssistantMessageFinal,
  isAssistantMessagePending,
  parseRequestErrorDetails,
  updateAssistantMessageWithError,
  updateLastAssistantMessage,
} from '../lib'
import type { Message, ParameterEnabled, PlaygroundConfig } from '../types'
import { useStreamRequest } from './use-stream-request'

type SessionMessageUpdater = (
  sessionId: string,
  updater: (previousMessages: Message[]) => Message[]
) => void

interface UseChatHandlerOptions {
  activeSessionId: string | null
  config: PlaygroundConfig
  parameterEnabled: ParameterEnabled
  onMessageUpdate: SessionMessageUpdater
}

const KNOWN_ERROR_MESSAGES = new Set<string>(Object.values(ERROR_MESSAGES))
const STREAM_UPDATE_FLUSH_MS = 50

type PendingStreamChunks = {
  content: string
  reasoning: string
}

type ChatRequestState = {
  abortController: AbortController | null
  generation: number
  isRequesting: boolean
  pending: PendingStreamChunks
  streamFlushTimer: number | null
}

function mergePendingStreamChunk(
  currentChunk: string,
  nextChunk: string
): string {
  if (!currentChunk || !nextChunk.startsWith(currentChunk)) {
    return currentChunk + nextChunk
  }

  return nextChunk
}

function createRequestState(): ChatRequestState {
  return {
    abortController: null,
    generation: 0,
    isRequesting: false,
    pending: { content: '', reasoning: '' },
    streamFlushTimer: null,
  }
}

/**
 * Handle chat requests per session. A request in one session never owns the
 * abort controller, stream, or loading state of another session.
 */
export function useChatHandler({
  activeSessionId,
  config,
  parameterEnabled,
  onMessageUpdate,
}: UseChatHandlerOptions) {
  const { t } = useTranslation()
  const { sendStreamRequest, stopStream, isStreamingFor } = useStreamRequest()
  const requestStatesRef = useRef<Map<string, ChatRequestState>>(new Map())
  const [, setActivityVersion] = useState(0)

  const getRequestState = useCallback((sessionId: string) => {
    const existing = requestStatesRef.current.get(sessionId)
    if (existing) return existing
    const created = createRequestState()
    requestStatesRef.current.set(sessionId, created)
    return created
  }, [])

  const discardPendingStreamUpdates = useCallback((state: ChatRequestState) => {
    if (state.streamFlushTimer !== null) {
      window.clearTimeout(state.streamFlushTimer)
      state.streamFlushTimer = null
    }
    state.pending = { content: '', reasoning: '' }
  }, [])

  const flushStreamUpdates = useCallback(
    (sessionId: string, generation: number) => {
      const state = getRequestState(sessionId)
      if (state.generation !== generation) return
      if (state.streamFlushTimer !== null) {
        window.clearTimeout(state.streamFlushTimer)
        state.streamFlushTimer = null
      }

      const pendingChunks = state.pending
      if (!pendingChunks.reasoning && !pendingChunks.content) return

      state.pending = { content: '', reasoning: '' }
      onMessageUpdate(sessionId, (previousMessages) => {
        if (state.generation !== generation) return previousMessages
        return updateLastAssistantMessage(previousMessages, (message) => {
          let updatedMessage = message

          if (pendingChunks.reasoning) {
            updatedMessage = applyStreamingChunk(
              updatedMessage,
              'reasoning',
              pendingChunks.reasoning
            )
          }

          if (pendingChunks.content) {
            updatedMessage = applyStreamingChunk(
              updatedMessage,
              'content',
              pendingChunks.content
            )
          }

          return updatedMessage
        })
      })
    },
    [getRequestState, onMessageUpdate]
  )

  const scheduleStreamFlush = useCallback(
    (sessionId: string, generation: number) => {
      const state = getRequestState(sessionId)
      if (state.generation !== generation || state.streamFlushTimer !== null) {
        return
      }

      state.streamFlushTimer = window.setTimeout(() => {
        state.streamFlushTimer = null
        flushStreamUpdates(sessionId, generation)
      }, STREAM_UPDATE_FLUSH_MS)
    },
    [flushStreamUpdates, getRequestState]
  )

  useEffect(
    () => () => {
      for (const [sessionId, state] of requestStatesRef.current) {
        state.generation += 1
        discardPendingStreamUpdates(state)
        state.abortController?.abort()
        state.abortController = null
        stopStream(sessionId)
      }
      requestStatesRef.current.clear()
    },
    [discardPendingStreamUpdates, stopStream]
  )

  const getDisplayError = useCallback(
    (error: string) => {
      if (KNOWN_ERROR_MESSAGES.has(error)) return t(error)

      const connectionClosedSuffix = `: ${ERROR_MESSAGES.CONNECTION_CLOSED}`
      if (error.endsWith(connectionClosedSuffix)) {
        return `${error.slice(0, -ERROR_MESSAGES.CONNECTION_CLOSED.length)}${t(
          ERROR_MESSAGES.CONNECTION_CLOSED
        )}`
      }

      return error
    },
    [t]
  )

  const handleStreamUpdate = useCallback(
    (
      sessionId: string,
      generation: number,
      type: 'reasoning' | 'content',
      chunk: string
    ) => {
      const state = getRequestState(sessionId)
      if (state.generation !== generation) return
      state.pending[type] = mergePendingStreamChunk(state.pending[type], chunk)
      scheduleStreamFlush(sessionId, generation)
    },
    [getRequestState, scheduleStreamFlush]
  )

  const handleStreamComplete = useCallback(
    (sessionId: string, generation: number) => {
      const state = getRequestState(sessionId)
      if (state.generation !== generation) return
      flushStreamUpdates(sessionId, generation)
      state.isRequesting = false
      setActivityVersion((version) => version + 1)
      onMessageUpdate(sessionId, (previousMessages) =>
        state.generation !== generation
          ? previousMessages
          : updateLastAssistantMessage(previousMessages, (message) =>
              isAssistantMessageFinal(message)
                ? message
                : completeAssistantMessage(message)
            )
      )
    },
    [flushStreamUpdates, getRequestState, onMessageUpdate]
  )

  const handleStreamError = useCallback(
    (
      sessionId: string,
      generation: number,
      error: string,
      errorCode?: string
    ) => {
      const state = getRequestState(sessionId)
      if (state.generation !== generation) return
      flushStreamUpdates(sessionId, generation)
      state.isRequesting = false
      setActivityVersion((version) => version + 1)
      const displayError = getDisplayError(error)
      toast.error(displayError)
      const errorTitle = t(ERROR_MESSAGES.API_REQUEST_ERROR)
      onMessageUpdate(sessionId, (previousMessages) =>
        state.generation !== generation
          ? previousMessages
          : updateAssistantMessageWithError(
              previousMessages,
              displayError,
              errorCode,
              errorTitle
            )
      )
    },
    [flushStreamUpdates, getDisplayError, getRequestState, onMessageUpdate, t]
  )

  const sendStreamingChat = useCallback(
    (sessionId: string, messages: Message[]) => {
      const state = getRequestState(sessionId)
      state.generation += 1
      const generation = state.generation
      state.abortController?.abort()
      state.abortController = null
      discardPendingStreamUpdates(state)
      state.isRequesting = true
      setActivityVersion((version) => version + 1)
      const payload = buildChatCompletionPayload(
        messages,
        config,
        parameterEnabled
      )
      const streamRequest = sendStreamRequest(
        payload,
        (type, chunk) => handleStreamUpdate(sessionId, generation, type, chunk),
        () => handleStreamComplete(sessionId, generation),
        (error, errorCode) =>
          handleStreamError(sessionId, generation, error, errorCode),
        sessionId
      )
      if (!streamRequest) {
        handleStreamError(
          sessionId,
          generation,
          ERROR_MESSAGES.STREAM_START_ERROR
        )
        return
      }

      void streamRequest.catch((error: unknown) => {
        const state = getRequestState(sessionId)
        if (state.generation !== generation) return
        const { errorCode, errorMessage } = parseRequestErrorDetails(error)
        handleStreamError(sessionId, generation, errorMessage, errorCode)
      })
    },
    [
      config,
      discardPendingStreamUpdates,
      getRequestState,
      handleStreamComplete,
      handleStreamError,
      handleStreamUpdate,
      parameterEnabled,
      sendStreamRequest,
    ]
  )

  const sendNonStreamingChat = useCallback(
    async (sessionId: string, messages: Message[]) => {
      const state = getRequestState(sessionId)
      const payload = buildChatCompletionPayload(
        messages,
        config,
        parameterEnabled
      )
      state.generation += 1
      const generation = state.generation
      const abortController = new AbortController()

      stopStream(sessionId)
      discardPendingStreamUpdates(state)
      state.abortController?.abort()
      state.abortController = abortController
      state.isRequesting = true
      setActivityVersion((version) => version + 1)

      try {
        const response = await sendChatCompletion(
          payload,
          abortController.signal
        )
        if (abortController.signal.aborted || state.generation !== generation) {
          return
        }

        if (!hasChatCompletionChoice(response)) {
          handleStreamError(
            sessionId,
            generation,
            ERROR_MESSAGES.API_REQUEST_ERROR
          )
          return
        }

        onMessageUpdate(sessionId, (previousMessages) => {
          if (state.generation !== generation) return previousMessages
          return updateLastAssistantMessage(
            previousMessages,
            (message) =>
              applyChatCompletionResponse(message, response) ?? message
          )
        })
        state.isRequesting = false
        setActivityVersion((version) => version + 1)
      } catch (error: unknown) {
        if (abortController.signal.aborted || state.generation !== generation) {
          return
        }

        const { errorCode, errorMessage } = parseRequestErrorDetails(error)
        handleStreamError(sessionId, generation, errorMessage, errorCode)
      } finally {
        if (state.generation === generation) {
          state.abortController = null
          state.isRequesting = false
          setActivityVersion((version) => version + 1)
        }
      }
    },
    [
      config,
      discardPendingStreamUpdates,
      getRequestState,
      handleStreamError,
      onMessageUpdate,
      parameterEnabled,
      stopStream,
    ]
  )

  const sendChat = useCallback(
    (sessionId: string, messages: Message[]) => {
      if (config.stream) {
        sendStreamingChat(sessionId, messages)
      } else {
        void sendNonStreamingChat(sessionId, messages)
      }
    },
    [config.stream, sendNonStreamingChat, sendStreamingChat]
  )

  const stopGeneration = useCallback(
    (sessionId: string) => {
      const state = getRequestState(sessionId)
      const stoppedGeneration = state.generation
      flushStreamUpdates(sessionId, stoppedGeneration)
      state.generation += 1
      discardPendingStreamUpdates(state)
      stopStream(sessionId)
      state.abortController?.abort()
      state.abortController = null
      state.isRequesting = false
      setActivityVersion((version) => version + 1)
      onMessageUpdate(sessionId, (previousMessages) =>
        updateLastAssistantMessage(previousMessages, (message) =>
          isAssistantMessagePending(message)
            ? completeAssistantMessage(message)
            : message
        )
      )
    },
    [
      discardPendingStreamUpdates,
      flushStreamUpdates,
      getRequestState,
      onMessageUpdate,
      stopStream,
    ]
  )

  const isGenerating = activeSessionId
    ? Boolean(
        getRequestState(activeSessionId).isRequesting ||
        isStreamingFor(activeSessionId)
      )
    : false

  return { sendChat, stopGeneration, isGenerating }
}
