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

interface UseImageGenerationOptions {
  config: PlaygroundConfig
  onMessageUpdate: (updater: (prev: Message[]) => Message[]) => void
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

/**
 * Hook for image mode: sends a prompt (optionally with reference images) to
 * the images API and appends the produced images to the assistant message.
 */
export function useImageGeneration({
  config,
  onMessageUpdate,
}: UseImageGenerationOptions) {
  const { t } = useTranslation()
  const [isGenerating, setIsGenerating] = useState(false)
  const abortControllerRef = useRef<AbortController | null>(null)
  const generationRef = useRef(0)

  useEffect(
    () => () => {
      generationRef.current += 1
      abortControllerRef.current?.abort()
      abortControllerRef.current = null
    },
    []
  )

  const stopGeneration = useCallback(() => {
    generationRef.current += 1
    abortControllerRef.current?.abort()
    abortControllerRef.current = null
    setIsGenerating(false)
    onMessageUpdate((prev) =>
      updateLastAssistantMessage(prev, (message) =>
        isAssistantMessagePending(message)
          ? completeAssistantMessage(message)
          : message
      )
    )
  }, [onMessageUpdate])

  const generateImage = useCallback(
    async (text: string, images?: string[]) => {
      const generation = generationRef.current + 1
      generationRef.current = generation
      abortControllerRef.current?.abort()
      const abortController = new AbortController()
      abortControllerRef.current = abortController

      onMessageUpdate((prev) => appendUserMessagePair(prev, text, images))
      setIsGenerating(true)

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

        if (
          abortController.signal.aborted ||
          generationRef.current !== generation
        ) {
          return
        }

        const rendered = (response.data ?? [])
          .map(toRenderableImage)
          .filter((url): url is string => Boolean(url))

        if (rendered.length === 0) {
          const errorTitle = t(ERROR_MESSAGES.API_REQUEST_ERROR)
          toast.error(t('No image was generated'))
          onMessageUpdate((prev) =>
            generationRef.current !== generation
              ? prev
              : updateAssistantMessageWithError(
                  prev,
                  t('No image was generated'),
                  undefined,
                  errorTitle
                )
          )
          return
        }

        onMessageUpdate((prev) =>
          generationRef.current !== generation
            ? prev
            : updateLastAssistantMessage(prev, (message) =>
                completeAssistantMessage({ ...message, images: rendered })
              )
        )
      } catch (error: unknown) {
        if (
          abortController.signal.aborted ||
          generationRef.current !== generation
        ) {
          return
        }
        const { errorCode, errorMessage } = parseRequestErrorDetails(error)
        toast.error(errorMessage)
        onMessageUpdate((prev) =>
          generationRef.current !== generation
            ? prev
            : updateAssistantMessageWithError(
                prev,
                errorMessage,
                errorCode,
                t(ERROR_MESSAGES.API_REQUEST_ERROR)
              )
        )
      } finally {
        if (generationRef.current === generation) {
          abortControllerRef.current = null
          setIsGenerating(false)
        }
      }
    },
    [config, onMessageUpdate, t]
  )

  return { generateImage, stopGeneration, isGenerating }
}
