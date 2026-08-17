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
import { useCallback, useEffect, useState } from 'react'

import { PLAYGROUND_MODES, type PlaygroundMode } from '../constants'
import {
  appendUserMessagePair,
  applyMessageEdit,
  createRegeneratedMessages,
  getMessageContent,
  getPreviousUserMessage,
  removeMessageByKey,
} from '../lib'
import type { Message } from '../types'

type SessionMessageUpdater = (
  sessionId: string,
  updater: (previousMessages: Message[]) => Message[]
) => void

type UsePlaygroundConversationOptions = {
  messages: Message[]
  mode: PlaygroundMode
  sessionId: string | null
  updateMessages: SessionMessageUpdater
  sendChat: (sessionId: string, messages: Message[]) => void
  sendImage: (
    sessionId: string,
    text: string,
    images?: string[],
    appendUserMessages?: boolean
  ) => void
}

export function usePlaygroundConversation({
  messages,
  mode,
  sessionId,
  updateMessages,
  sendChat,
  sendImage,
}: UsePlaygroundConversationOptions) {
  const [editingMessageKey, setEditingMessageKey] = useState<string | null>(
    null
  )

  useEffect(() => {
    setEditingMessageKey(null)
  }, [sessionId])

  const handleSendMessage = useCallback(
    (text: string, images?: string[]) => {
      if (!sessionId) return
      const nextMessages = appendUserMessagePair(messages, text, images)
      updateMessages(sessionId, () => nextMessages)
      if (mode === PLAYGROUND_MODES.IMAGE) {
        sendImage(sessionId, text, images, false)
      } else {
        sendChat(sessionId, nextMessages)
      }
    },
    [messages, mode, sendChat, sendImage, sessionId, updateMessages]
  )

  const handleRegenerateMessage = useCallback(
    (message: Message) => {
      if (!sessionId) return
      const nextMessages = createRegeneratedMessages(messages, message.key)
      if (!nextMessages) return

      updateMessages(sessionId, () => nextMessages)
      if (mode === PLAYGROUND_MODES.IMAGE) {
        const messageIndex = messages.findIndex(
          (candidate) => candidate.key === message.key
        )
        const userMessage =
          message.from === 'user'
            ? message
            : getPreviousUserMessage(messages, messageIndex)
        if (userMessage) {
          sendImage(
            sessionId,
            getMessageContent(userMessage),
            userMessage.images,
            false
          )
        }
      } else {
        sendChat(sessionId, nextMessages)
      }
    },
    [messages, mode, sendChat, sendImage, sessionId, updateMessages]
  )

  const handleEditMessage = useCallback((message: Message) => {
    setEditingMessageKey(message.key)
  }, [])

  const handleEditOpenChange = useCallback((open: boolean) => {
    if (!open) {
      setEditingMessageKey(null)
    }
  }, [])

  const applyEdit = useCallback(
    (newContent: string, shouldSubmit: boolean) => {
      if (!editingMessageKey || !sessionId) return

      const editResult = applyMessageEdit(
        messages,
        editingMessageKey,
        newContent,
        shouldSubmit
      )
      if (!editResult) return

      setEditingMessageKey(null)
      updateMessages(sessionId, () => editResult.messages)

      if (editResult.shouldSend) {
        if (mode === PLAYGROUND_MODES.IMAGE) {
          const editedMessage = editResult.messages.find(
            (message) => message.key === editingMessageKey
          )
          if (editedMessage) {
            sendImage(
              sessionId,
              getMessageContent(editedMessage),
              editedMessage.images,
              false
            )
          }
        } else {
          sendChat(sessionId, editResult.messages)
        }
      }
    },
    [
      editingMessageKey,
      messages,
      mode,
      sendChat,
      sendImage,
      sessionId,
      updateMessages,
    ]
  )

  const handleDeleteMessage = useCallback(
    (message: Message) => {
      if (!sessionId) return
      updateMessages(sessionId, (previousMessages) =>
        removeMessageByKey(previousMessages, message.key)
      )
    },
    [sessionId, updateMessages]
  )

  return {
    editingMessageKey,
    handleSendMessage,
    handleRegenerateMessage,
    handleEditMessage,
    handleEditOpenChange,
    applyEdit,
    handleDeleteMessage,
  }
}
