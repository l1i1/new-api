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
import { MessagesSquare } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Sheet, SheetContent } from '@/components/ui/sheet'

import { PlaygroundChat } from './components/chat/playground-chat'
import { PlaygroundInput } from './components/input/playground-input'
import { PlaygroundSessionList } from './components/sessions/playground-session-list'
import { PLAYGROUND_MODES, type PlaygroundMode } from './constants'
import {
  useChatHandler,
  useImageGeneration,
  usePlaygroundConversation,
  usePlaygroundOptions,
  usePlaygroundState,
} from './hooks'

export function Playground() {
  const { t } = useTranslation()
  const [mobileSessionsOpen, setMobileSessionsOpen] = useState(false)
  const [mode, setMode] = useState<PlaygroundMode>(PLAYGROUND_MODES.CHAT)
  const {
    config,
    parameterEnabled,
    sessions,
    activeSessionId,
    messages,
    isLoadingSessions,
    models,
    groups,
    updateMessages,
    setModels,
    setGroups,
    updateConfig,
    updateParameterEnabled,
    createSession,
    selectSession,
    deleteSession,
    clearMessages,
  } = usePlaygroundState()

  const { sendChat, stopGeneration, isGenerating } = useChatHandler({
    config,
    parameterEnabled,
    onMessageUpdate: updateMessages,
  })

  const {
    generateImage,
    stopGeneration: stopImageGeneration,
    isGenerating: isGeneratingImage,
  } = useImageGeneration({
    config,
    onMessageUpdate: updateMessages,
  })

  const {
    editingMessageKey,
    handleSendMessage,
    handleRegenerateMessage,
    handleEditMessage,
    handleEditOpenChange,
    applyEdit,
    handleDeleteMessage,
  } = usePlaygroundConversation({
    messages,
    updateMessages,
    sendChat,
  })

  const isBusy = isGenerating || isGeneratingImage
  const handleStop = isGeneratingImage ? stopImageGeneration : stopGeneration
  const handleSubmit =
    mode === PLAYGROUND_MODES.IMAGE ? generateImage : handleSendMessage

  const handleModeChange = (nextMode: PlaygroundMode) => {
    if (isBusy) return
    setMode(nextMode)
  }

  const handleClearMessages = () => {
    handleEditOpenChange(false)
    clearMessages()
  }

  const { isLoadingModels } = usePlaygroundOptions({
    currentGroup: config.group,
    currentModel: config.model,
    setGroups,
    setModels,
    updateConfig,
  })

  const sessionListProps = {
    sessions,
    activeSessionId,
    onCreateSession: createSession,
    onSelectSession: selectSession,
    onDeleteSession: deleteSession,
  }

  return (
    <div className='relative flex size-full min-h-0 overflow-hidden'>
      <aside className='border-border hidden w-60 shrink-0 border-r md:block'>
        <PlaygroundSessionList {...sessionListProps} />
      </aside>

      <div className='relative flex min-w-0 flex-1 flex-col overflow-hidden'>
        <div className='border-border flex items-center border-b px-2 py-1.5 md:hidden'>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            className='h-7 gap-1.5 px-2 text-xs'
            onClick={() => setMobileSessionsOpen(true)}
          >
            <MessagesSquare className='size-3.5' aria-hidden='true' />
            {t('Chats')}
          </Button>
        </div>

        {/* Full-width scroll container: scrolling works even over side whitespace */}
        <div className='flex min-h-0 flex-1 flex-col overflow-hidden'>
          <PlaygroundChat
            messages={messages}
            isLoadingMessages={isLoadingSessions}
            onRegenerateMessage={handleRegenerateMessage}
            onEditMessage={handleEditMessage}
            onDeleteMessage={handleDeleteMessage}
            onSelectPrompt={handleSendMessage}
            isGenerating={isGenerating}
            editingKey={editingMessageKey}
            onCancelEdit={handleEditOpenChange}
            onSaveEdit={(newContent) => applyEdit(newContent, false)}
            onSaveEditAndSubmit={(newContent) => applyEdit(newContent, true)}
          />
        </div>

        {/* Input area: center content and constrain to the same container width */}
        <div className='mx-auto w-full max-w-4xl'>
          <PlaygroundInput
            config={config}
            disabled={isBusy}
            groups={groups}
            groupValue={config.group}
            isGenerating={isBusy}
            isModelLoading={isLoadingModels}
            mode={mode}
            modelValue={config.model}
            models={models}
            onGroupChange={(value) => updateConfig('group', value)}
            onConfigChange={updateConfig}
            onClearMessages={handleClearMessages}
            onModeChange={handleModeChange}
            onModelChange={(value) => updateConfig('model', value)}
            onParameterEnabledChange={updateParameterEnabled}
            onStop={handleStop}
            onSubmit={handleSubmit}
            parameterEnabled={parameterEnabled}
            hasMessages={messages.length > 0}
          />
        </div>
      </div>

      <Sheet open={mobileSessionsOpen} onOpenChange={setMobileSessionsOpen}>
        <SheetContent side='left' className='w-72 p-0'>
          <PlaygroundSessionList
            {...sessionListProps}
            onNavigate={() => setMobileSessionsOpen(false)}
          />
        </SheetContent>
      </Sheet>
    </div>
  )
}
