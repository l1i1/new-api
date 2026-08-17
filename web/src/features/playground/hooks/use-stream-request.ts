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
import { SSE } from 'sse.js'

import { getFreshAuthHeaders } from '@/lib/api'

import { API_ENDPOINTS, ERROR_MESSAGES } from '../constants'
import {
  getStreamReadyStateError,
  isStreamClosedReadyState,
  isStreamDoneMessage,
  parseStreamErrorDetails,
  parseStreamMessageUpdates,
} from '../lib'
import type { ChatCompletionRequest } from '../types'

interface StreamEventSource {
  readyState?: number
  addEventListener: (
    type: string,
    listener: (event: Event & { data?: string; readyState?: number }) => void
  ) => void
  close: () => void
  stream: () => void
}

interface StreamRequestCallbacks {
  onUpdate: (type: 'reasoning' | 'content', chunk: string) => void
  onComplete: () => void
  onError: (error: string, errorCode?: string) => void
}

interface StreamRequestControllerRuntime {
  getHeaders: () => Promise<Record<string, string>>
  createSource: (
    payload: ChatCompletionRequest,
    headers: Record<string, string>
  ) => StreamEventSource
  setStreaming: (streaming: boolean, requestKey?: string) => void
}

type ActiveStream = {
  generation: number
  source: StreamEventSource | null
}

function getStreamStartErrorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : ERROR_MESSAGES.STREAM_START_ERROR
}

export function createStreamRequestController(
  runtime: StreamRequestControllerRuntime
) {
  const activeStreams = new Map<string, ActiveStream>()
  const generations = new Map<string, number>()

  const getGeneration = (requestKey: string) =>
    (generations.get(requestKey) ?? 0) + 1

  const closeActiveSource = (requestKey: string, target: StreamEventSource) => {
    target.close()
    const active = activeStreams.get(requestKey)
    if (active?.source === target) {
      activeStreams.delete(requestKey)
      runtime.setStreaming(false, requestKey)
    }
  }

  const send = async (
    payload: ChatCompletionRequest,
    callbacks: StreamRequestCallbacks,
    requestKey = 'default'
  ) => {
    const requestGeneration = getGeneration(requestKey)
    generations.set(requestKey, requestGeneration)
    const previous = activeStreams.get(requestKey)
    activeStreams.set(requestKey, {
      generation: requestGeneration,
      source: null,
    })
    previous?.source?.close()
    runtime.setStreaming(false, requestKey)

    let headers: Record<string, string>
    try {
      headers = await runtime.getHeaders()
    } catch (error: unknown) {
      if (generations.get(requestKey) !== requestGeneration) return
      activeStreams.delete(requestKey)
      callbacks.onError(getStreamStartErrorMessage(error))
      return
    }
    if (generations.get(requestKey) !== requestGeneration) return

    let nextSource: StreamEventSource
    try {
      nextSource = runtime.createSource(payload, headers)
    } catch (error: unknown) {
      if (generations.get(requestKey) !== requestGeneration) return
      activeStreams.delete(requestKey)
      runtime.setStreaming(false, requestKey)
      callbacks.onError(getStreamStartErrorMessage(error))
      return
    }

    activeStreams.set(requestKey, {
      generation: requestGeneration,
      source: nextSource,
    })
    runtime.setStreaming(true, requestKey)
    let completed = false

    const isCurrent = () => {
      const active = activeStreams.get(requestKey)
      return (
        generations.get(requestKey) === requestGeneration &&
        active?.generation === requestGeneration &&
        active.source === nextSource
      )
    }

    const handleError = (errorMessage: string, errorCode?: string) => {
      if (!isCurrent() || completed) return
      completed = true
      callbacks.onError(errorMessage, errorCode)
      closeActiveSource(requestKey, nextSource)
    }

    nextSource.addEventListener('message', (event) => {
      if (!isCurrent() || completed) return
      const data = event.data ?? ''
      if (isStreamDoneMessage(data)) {
        completed = true
        closeActiveSource(requestKey, nextSource)
        callbacks.onComplete()
        return
      }

      try {
        const updates = parseStreamMessageUpdates(data)

        for (const update of updates) {
          callbacks.onUpdate(update.type, update.chunk)
        }
      } catch (error) {
        // eslint-disable-next-line no-console
        console.error('Failed to parse SSE message:', error)
        handleError(ERROR_MESSAGES.PARSE_ERROR)
      }
    })

    nextSource.addEventListener('error', (event) => {
      if (!isCurrent() || completed) return
      if (!isStreamClosedReadyState(nextSource.readyState)) {
        // eslint-disable-next-line no-console
        console.error('SSE Error:', event)
        const { errorCode, errorMessage } = parseStreamErrorDetails(event.data)
        handleError(errorMessage, errorCode)
      }
    })

    nextSource.addEventListener('readystatechange', (event) => {
      if (!isCurrent() || completed) return
      const errorMessage = getStreamReadyStateError(
        event.readyState,
        nextSource
      )

      if (errorMessage) {
        handleError(errorMessage)
      }
    })

    try {
      if (!isCurrent()) return
      nextSource.stream()
    } catch (error: unknown) {
      if (!isCurrent() || completed) return
      // eslint-disable-next-line no-console
      console.error('Failed to start SSE stream:', error)
      handleError(ERROR_MESSAGES.STREAM_START_ERROR)
    }
  }

  const cancel = (requestKey: string, notify: boolean) => {
    const nextGeneration = getGeneration(requestKey)
    generations.set(requestKey, nextGeneration)
    const active = activeStreams.get(requestKey)
    activeStreams.delete(requestKey)
    active?.source?.close()
    if (notify) runtime.setStreaming(false, requestKey)
  }

  const stop = (requestKey = 'default') => cancel(requestKey, true)
  const dispose = () => {
    for (const requestKey of new Set([
      ...generations.keys(),
      ...activeStreams.keys(),
    ])) {
      cancel(requestKey, false)
    }
  }

  return { send, stop, dispose }
}

/**
 * Hook for handling streaming chat completion requests. A request key keeps
 * streams for separate playground sessions independent.
 */
export function useStreamRequest() {
  const [streamingKeys, setStreamingKeys] = useState<Set<string>>(
    () => new Set()
  )
  const controllerRef = useRef<ReturnType<
    typeof createStreamRequestController
  > | null>(null)
  if (!controllerRef.current) {
    controllerRef.current = createStreamRequestController({
      getHeaders: getFreshAuthHeaders,
      createSource: (payload, headers) =>
        new SSE(API_ENDPOINTS.CHAT_COMPLETIONS, {
          headers,
          method: 'POST',
          payload: JSON.stringify(payload),
        }) as StreamEventSource,
      setStreaming: (streaming, requestKey = 'default') => {
        setStreamingKeys((previousKeys) => {
          const nextKeys = new Set(previousKeys)
          if (streaming) {
            nextKeys.add(requestKey)
          } else {
            nextKeys.delete(requestKey)
          }
          return nextKeys
        })
      },
    })
  }

  const sendStreamRequest = useCallback(
    (
      payload: ChatCompletionRequest,
      onUpdate: (type: 'reasoning' | 'content', chunk: string) => void,
      onComplete: () => void,
      onError: (error: string, errorCode?: string) => void,
      requestKey = 'default'
    ) =>
      controllerRef.current?.send(
        payload,
        {
          onUpdate,
          onComplete,
          onError,
        },
        requestKey
      ),
    []
  )

  const stopStream = useCallback((requestKey = 'default') => {
    controllerRef.current?.stop(requestKey)
  }, [])

  useEffect(
    () => () => {
      controllerRef.current?.dispose()
    },
    []
  )

  const isStreamingFor = useCallback(
    (requestKey: string) => streamingKeys.has(requestKey),
    [streamingKeys]
  )

  return {
    sendStreamRequest,
    stopStream,
    isStreaming: streamingKeys.has('default'),
    isStreamingFor,
  }
}
