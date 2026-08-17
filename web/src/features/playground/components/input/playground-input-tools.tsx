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
import { ImagePlus, Trash2Icon } from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  PromptInputButton,
  PromptInputTools,
} from '@/components/ai-elements/prompt-input'
import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

import { PLAYGROUND_MODES, type PlaygroundMode } from '../../constants'
import { MAX_IMAGE_ATTACHMENTS } from '../../lib'
import type { ParameterEnabled, PlaygroundConfig } from '../../types'
import { PlaygroundParameterPanel } from './playground-parameter-panel'

type PlaygroundInputToolsProps = {
  attachmentCount: number
  config: PlaygroundConfig
  disabled?: boolean
  hasMessages?: boolean
  mode: PlaygroundMode
  onAttachImages: (files: FileList | File[]) => void
  onClearMessages?: () => void
  onConfigChange: <K extends keyof PlaygroundConfig>(
    key: K,
    value: PlaygroundConfig[K]
  ) => void
  onModeChange: (mode: PlaygroundMode) => void
  onParameterEnabledChange: (
    key: keyof ParameterEnabled,
    value: boolean
  ) => void
  parameterEnabled: ParameterEnabled
}

const MODE_OPTIONS: { value: PlaygroundMode; labelKey: string }[] = [
  { value: PLAYGROUND_MODES.CHAT, labelKey: 'Chat' },
  { value: PLAYGROUND_MODES.IMAGE, labelKey: 'Image generation' },
]

export function PlaygroundInputTools({
  attachmentCount,
  config,
  disabled,
  hasMessages = false,
  mode,
  onAttachImages,
  onClearMessages,
  onConfigChange,
  onModeChange,
  onParameterEnabledChange,
  parameterEnabled,
}: PlaygroundInputToolsProps) {
  const { t } = useTranslation()
  const [clearConfirmOpen, setClearConfirmOpen] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleFileChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    if (event.target.files?.length) {
      onAttachImages(event.target.files)
    }
    event.target.value = ''
  }

  const handleClearMessages = () => {
    onClearMessages?.()
    setClearConfirmOpen(false)
    toast.success(t('Conversation cleared'))
  }

  return (
    <>
      <PromptInputTools className='bg-background/70 border-border/60 rounded-lg border p-1 shadow-xs'>
        <div
          role='group'
          aria-label={t('Mode')}
          className='border-border inline-flex h-7 items-center border p-0.5'
        >
          {MODE_OPTIONS.map((option) => (
            <button
              key={option.value}
              type='button'
              aria-pressed={mode === option.value}
              onClick={() => onModeChange(option.value)}
              className={cn(
                'inline-flex h-full items-center justify-center rounded-none px-2 text-xs font-medium transition-colors',
                mode === option.value
                  ? 'bg-foreground text-background'
                  : 'text-muted-foreground hover:text-foreground'
              )}
            >
              {t(option.labelKey)}
            </button>
          ))}
        </div>

        <Tooltip>
          <TooltipTrigger
            render={
              <PromptInputButton
                aria-label={t('Attach image')}
                className='text-muted-foreground hover:text-foreground hover:bg-muted/70 font-medium'
                disabled={disabled || attachmentCount >= MAX_IMAGE_ATTACHMENTS}
                onClick={() => fileInputRef.current?.click()}
                variant='ghost'
              >
                <ImagePlus size={16} />
              </PromptInputButton>
            }
          />
          <TooltipContent>
            <p>{t('Attach image')}</p>
          </TooltipContent>
        </Tooltip>
        <input
          ref={fileInputRef}
          type='file'
          accept='image/*'
          multiple
          className='hidden'
          onChange={handleFileChange}
        />

        {mode === PLAYGROUND_MODES.CHAT && (
          <PlaygroundParameterPanel
            config={config}
            disabled={disabled}
            onConfigChange={onConfigChange}
            onParameterEnabledChange={onParameterEnabledChange}
            parameterEnabled={parameterEnabled}
          />
        )}

        <Tooltip>
          <TooltipTrigger
            render={
              <PromptInputButton
                aria-label={t('Clear chat history')}
                className='text-muted-foreground hover:text-destructive hover:bg-destructive/10 font-medium'
                disabled={disabled || !hasMessages || !onClearMessages}
                onClick={() => setClearConfirmOpen(true)}
                variant='ghost'
              >
                <Trash2Icon size={16} />
              </PromptInputButton>
            }
          />
          <TooltipContent>
            <p>{t('Clear chat history')}</p>
          </TooltipContent>
        </Tooltip>
      </PromptInputTools>

      <ConfirmDialog
        destructive
        desc={t(
          'All playground messages saved in this browser will be removed. This cannot be undone.'
        )}
        confirmText={t('Clear')}
        handleConfirm={handleClearMessages}
        open={clearConfirmOpen}
        onOpenChange={setClearConfirmOpen}
        title={t('Clear chat history?')}
      />
    </>
  )
}
