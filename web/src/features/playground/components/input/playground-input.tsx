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
import { X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  PromptInput,
  PromptInputFooter,
  PromptInputTextarea,
  type PromptInputMessage,
} from '@/components/ai-elements/prompt-input'

import type { PlaygroundMode } from '../../constants'
import { MAX_IMAGE_ATTACHMENTS, fileToImageDataUrl } from '../../lib'
import type {
  ModelOption,
  GroupOption,
  ParameterEnabled,
  PlaygroundConfig,
} from '../../types'
import { PlaygroundInputControls } from './playground-input-controls'
import { PlaygroundInputTools } from './playground-input-tools'

interface PlaygroundInputProps {
  config: PlaygroundConfig
  mode: PlaygroundMode
  onModeChange: (mode: PlaygroundMode) => void
  onSubmit: (text: string, images?: string[]) => void
  onStop?: () => void
  disabled?: boolean
  isGenerating?: boolean
  models: ModelOption[]
  modelValue: string
  onModelChange: (value: string) => void
  isModelLoading?: boolean
  groups: GroupOption[]
  groupValue: string
  onGroupChange: (value: string) => void
  hasMessages?: boolean
  onConfigChange: <K extends keyof PlaygroundConfig>(
    key: K,
    value: PlaygroundConfig[K]
  ) => void
  onClearMessages?: () => void
  onParameterEnabledChange: (
    key: keyof ParameterEnabled,
    value: boolean
  ) => void
  parameterEnabled: ParameterEnabled
}

export function PlaygroundInput({
  config,
  mode,
  onModeChange,
  groups,
  groupValue,
  onGroupChange,
  hasMessages = false,
  onConfigChange,
  onClearMessages,
  onParameterEnabledChange,
  parameterEnabled,
  onSubmit,
  onStop,
  disabled,
  isGenerating,
  models,
  modelValue,
  onModelChange,
  isModelLoading = false,
}: PlaygroundInputProps) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const [attachments, setAttachments] = useState<string[]>([])

  const handleSubmit = (message: PromptInputMessage) => {
    if (disabled) return
    const hasText = Boolean(message.text?.trim())
    if (!hasText && attachments.length === 0) return

    onSubmit(message.text ?? '', attachments.length ? attachments : undefined)
    setText('')
    setAttachments([])
  }

  const handleAttachImages = async (files: FileList | File[]) => {
    const remaining = MAX_IMAGE_ATTACHMENTS - attachments.length
    if (remaining <= 0) return

    const selected = [...files].slice(0, remaining)
    for (const file of selected) {
      try {
        const dataUrl = await fileToImageDataUrl(file)
        setAttachments((prev) =>
          prev.length >= MAX_IMAGE_ATTACHMENTS ? prev : [...prev, dataUrl]
        )
      } catch {
        toast.error(t('Failed to attach image'))
      }
    }
  }

  const handleRemoveAttachment = (index: number) => {
    setAttachments((prev) => prev.filter((_, i) => i !== index))
  }

  return (
    <div className='grid shrink-0 gap-4 px-1 md:pb-4'>
      <PromptInput
        accept='image/*'
        className='relative'
        groupClassName='bg-background border-border overflow-hidden rounded-none border transition-colors focus-within:border-foreground/40'
        maxFiles={MAX_IMAGE_ATTACHMENTS}
        onFilesAdded={(files) => void handleAttachImages(files)}
        onSubmit={handleSubmit}
      >
        {attachments.length > 0 && (
          <div
            className='flex w-full flex-wrap justify-start gap-2 px-3 pt-3 pb-1'
            data-align='block-start'
          >
            {attachments.map((src, index) => (
              <div
                key={`${src.length}-${src.slice(30, 46)}`}
                className='group border-border relative size-14 overflow-hidden border'
              >
                <img
                  src={src}
                  alt={t('Attached image')}
                  className='size-full object-cover'
                />
                <button
                  type='button'
                  aria-label={t('Remove image')}
                  onClick={() => handleRemoveAttachment(index)}
                  className='bg-background/90 text-muted-foreground hover:text-foreground absolute top-0.5 right-0.5 flex size-4 items-center justify-center opacity-0 transition-opacity group-hover:opacity-100'
                >
                  <X className='size-3' aria-hidden='true' />
                </button>
              </div>
            ))}
          </div>
        )}

        <PromptInputTextarea
          autoComplete='off'
          autoCorrect='off'
          autoCapitalize='off'
          spellCheck={false}
          className='min-h-20 px-5 pt-4 pb-3 leading-7 md:min-h-24 md:text-base'
          disabled={disabled}
          onChange={(event) => setText(event.target.value)}
          placeholder={t('Ask anything')}
          value={text}
        />

        <PromptInputFooter className='border-border/60 bg-muted/20 dark:bg-muted/10 border-t px-3 py-2.5'>
          <PlaygroundInputControls
            disabled={disabled}
            groups={groups}
            groupValue={groupValue}
            hasAttachments={attachments.length > 0}
            isGenerating={isGenerating}
            isModelLoading={isModelLoading}
            models={models}
            modelValue={modelValue}
            onGroupChange={onGroupChange}
            onModelChange={onModelChange}
            onStop={onStop}
            text={text}
            tools={
              <PlaygroundInputTools
                attachmentCount={attachments.length}
                config={config}
                disabled={disabled}
                hasMessages={hasMessages}
                mode={mode}
                onAttachImages={(files) => void handleAttachImages(files)}
                onClearMessages={onClearMessages}
                onConfigChange={onConfigChange}
                onModeChange={onModeChange}
                onParameterEnabledChange={onParameterEnabledChange}
                parameterEnabled={parameterEnabled}
              />
            }
          />
        </PromptInputFooter>
      </PromptInput>
    </div>
  )
}
