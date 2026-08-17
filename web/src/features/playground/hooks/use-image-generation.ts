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

import { editImages, generateImages } from '../api'
import { ERROR_MESSAGES } from '../constants'
import {
  appendUserMessagePair,
  completeAssistantMessage,
  dataUrlToFile,
  isAssistantMessagePending,
  parseRequestErrorDetails,
  updateAssistantMessageWithError,
  updateLastAssistantMessage,
} from '../lib'
import type { ImageGenerationResult, Message, PlaygroundConfig } from '../types'

type SessionMessageUpdater = (
  sessionId: string,
  updater: (previousMessages: Message[]) => Message[]
) => void

interface UseImageGenerationOptions {
  activeSessionId: string | null
  config: PlaygroundConfig
  onMessageUpdate: SessionMessageUpdater
}

type ImageRequestState = {
  abortController: AbortController | null
  generation: number
  isGenerating: boolean
}

/** Normalize an API image result to a renderable URL (data URL or remote). */
function toRenderableImage(result: ImageGenerationResult): string | null {
  if (result.b64_json) {
    return `data:image/png;base64,${result.b64_json}`
  }
  return result.url ?? null
}

async function buildImageEditsFormData(
  config: PlaygroundConfig,
  prompt: string,
  images: string[]
): Promise<FormData> {
  const formData = new FormData()
  formData.append('model', config.model)
  formData.append('prompt', prompt)
  if (config.group) {
    formData.append('group', config.group)
  }
  const files = await Promise.all(
    images.map((dataUrl, index) => dataUrlToFile(dataUrl, `image-${index}`))
  )
  for (const file of files) {
    formData.append('image', file)
  }
  return formData
}

function createRequestState(): ImageRequestState {
  return { abortController: null, generation: 0, isGenerating: false }
}

/**
 * Handle image requests per session so switching conversations does not
 * cancel or disable an unrelated conversation.
 */
export function useImageGeneration({
  activeSessionId,
  config,
  onMessageUpdate,
}: UseImageGenerationOptions) {
  const { t } = useTranslation()
  const requestStatesRef = useRef<Map<string, ImageRequestState>>(new Map())
  const [, setActivityVersion] = useState(0)

  const getRequestState = useCallback((sessionId: string) => {
    const existing = requestStatesRef.current.get(sessionId)
    if (existing) return existing
    const created = createRequestState()
    requestStatesRef.current.set(sessionId, created)
    return created
  }, [])

  useEffect(
    () => () => {
      for (const state of requestStatesRef.current.values()) {
        state.generation += 1
        state.abortController?.abort()
      }
      requestStatesRef.current.clear()
    },
    []
  )

  const stopGeneration = useCallback(
    (sessionId: string) => {
      const state = getRequestState(sessionId)
      state.generation += 1
      state.abortController?.abort()
      state.abortController = null
      state.isGenerating = false
      setActivityVersion((version) => version + 1)
      onMessageUpdate(sessionId, (previousMessages) =>
        updateLastAssistantMessage(previousMessages, (message) =>
          isAssistantMessagePending(message)
            ? completeAssistantMessage(message)
            : message
        )
      )
    },
    [getRequestState, onMessageUpdate]
  )

  const generateImage = useCallback(
    async (
      sessionId: string,
      text: string,
      images?: string[],
      appendUserMessages = true
    ) => {
      const state = getRequestState(sessionId)
      state.generation += 1
      const generation = state.generation
      state.abortController?.abort()
      const abortController = new AbortController()
      state.abortController = abortController
      state.isGenerating = true
      setActivityVersion((version) => version + 1)

      if (appendUserMessages) {
        onMessageUpdate(sessionId, (previousMessages) =>
          appendUserMessagePair(previousMessages, text, images)
        )
      }

      try {
        const response = images?.length
          ? await editImages(
              await buildImageEditsFormData(config, text, images),
              abortController.signal
            )
          : await generateImages(
              {
                model: config.model,
                prompt: text,
                group: config.group || undefined,
              },
              abortController.signal
            )

        if (abortController.signal.aborted || state.generation !== generation) {
          return
        }

        const rendered = (response.data ?? [])
          .map(toRenderableImage)
          .filter((url): url is string => Boolean(url))

        if (rendered.length === 0) {
          const errorTitle = t(ERROR_MESSAGES.API_REQUEST_ERROR)
          toast.error(t('No image was generated'))
          onMessageUpdate(sessionId, (previousMessages) =>
            state.generation !== generation
              ? previousMessages
              : updateAssistantMessageWithError(
                  previousMessages,
                  t('No image was generated'),
                  undefined,
                  errorTitle
                )
          )
          return
        }

        onMessageUpdate(sessionId, (previousMessages) =>
          state.generation !== generation
            ? previousMessages
            : updateLastAssistantMessage(previousMessages, (message) =>
                completeAssistantMessage({ ...message, images: rendered })
              )
        )
      } catch (error: unknown) {
        if (abortController.signal.aborted || state.generation !== generation) {
          return
        }
        const { errorCode, errorMessage } = parseRequestErrorDetails(error)
        toast.error(errorMessage)
        onMessageUpdate(sessionId, (previousMessages) =>
          state.generation !== generation
            ? previousMessages
            : updateAssistantMessageWithError(
                previousMessages,
                errorMessage,
                errorCode,
                t(ERROR_MESSAGES.API_REQUEST_ERROR)
              )
        )
      } finally {
        if (state.generation === generation) {
          state.abortController = null
          state.isGenerating = false
          setActivityVersion((version) => version + 1)
        }
      }
    },
    [config, getRequestState, onMessageUpdate, t]
  )

  const isGenerating = activeSessionId
    ? Boolean(getRequestState(activeSessionId).isGenerating)
    : false

  return { generateImage, stopGeneration, isGenerating }
}
