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
import assert from 'node:assert/strict'
import { beforeEach, describe, test } from 'node:test'

import {
  DEFAULT_CONFIG,
  DEFAULT_PARAMETER_ENABLED,
  PLAYGROUND_MODES,
  STORAGE_KEYS,
} from '../../../constants'
import type { PlaygroundSession } from '../../../types'
import { loadSessions, saveSessions } from '../storage'

class MemoryStorage implements Storage {
  private values = new Map<string, string>()
  failWritesFor = new Set<string>()

  get length() {
    return this.values.size
  }

  clear() {
    this.values.clear()
    this.failWritesFor.clear()
  }

  getItem(key: string) {
    return this.values.get(key) ?? null
  }

  key(index: number) {
    return [...this.values.keys()][index] ?? null
  }

  removeItem(key: string) {
    this.values.delete(key)
  }

  setItem(key: string, value: string) {
    if (this.failWritesFor.has(key)) {
      throw new Error(`write failed for ${key}`)
    }
    this.values.set(key, value)
  }
}

const memoryStorage = new MemoryStorage()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: memoryStorage,
})

const legacyMessage = {
  key: 'message-1',
  from: 'user' as const,
  versions: [{ id: 'version-1', content: 'hello' }],
}

function createSession(
  id: string,
  mode: PlaygroundSession['mode']
): PlaygroundSession {
  const now = Date.now()
  return {
    id,
    title: id,
    createdAt: now,
    updatedAt: now,
    mode,
    config: { ...DEFAULT_CONFIG, model: id },
    parameterEnabled: { ...DEFAULT_PARAMETER_ENABLED },
    messages: [],
  }
}

describe('playground session storage', () => {
  beforeEach(() => memoryStorage.clear())

  test('migrates legacy messages with default per-session controls', () => {
    memoryStorage.setItem(
      STORAGE_KEYS.MESSAGES,
      JSON.stringify({ version: 1, data: [legacyMessage] })
    )

    const sessions = loadSessions()

    assert.equal(sessions?.length, 1)
    assert.equal(sessions?.[0]?.mode, PLAYGROUND_MODES.CHAT)
    assert.equal(sessions?.[0]?.config.model, DEFAULT_CONFIG.model)
    assert.deepEqual(sessions?.[0]?.parameterEnabled, DEFAULT_PARAMETER_ENABLED)
    assert.equal(memoryStorage.getItem(STORAGE_KEYS.MESSAGES), null)
    assert.notEqual(memoryStorage.getItem(STORAGE_KEYS.SESSIONS), null)
  })

  test('round-trips independent mode and model settings for each session', () => {
    saveSessions([
      createSession('chat-session', PLAYGROUND_MODES.CHAT),
      createSession('image-session', PLAYGROUND_MODES.IMAGE),
    ])

    const sessions = loadSessions()

    assert.equal(sessions?.[0]?.mode, PLAYGROUND_MODES.CHAT)
    assert.equal(sessions?.[0]?.config.model, 'chat-session')
    assert.equal(sessions?.[1]?.mode, PLAYGROUND_MODES.IMAGE)
    assert.equal(sessions?.[1]?.config.model, 'image-session')
  })

  test('keeps legacy messages when migrated session storage cannot be written', () => {
    memoryStorage.setItem(
      STORAGE_KEYS.MESSAGES,
      JSON.stringify({ version: 1, data: [legacyMessage] })
    )
    memoryStorage.failWritesFor.add(STORAGE_KEYS.SESSIONS)

    const sessions = loadSessions()

    assert.equal(sessions?.length, 1)
    assert.notEqual(memoryStorage.getItem(STORAGE_KEYS.MESSAGES), null)
    assert.equal(memoryStorage.getItem(STORAGE_KEYS.SESSIONS), null)
  })

  test('falls back to legacy messages when the session store is corrupt', () => {
    memoryStorage.setItem(STORAGE_KEYS.SESSIONS, '{"version":1,"data":42}')
    memoryStorage.setItem(
      STORAGE_KEYS.MESSAGES,
      JSON.stringify({ version: 1, data: [legacyMessage] })
    )

    const sessions = loadSessions()

    assert.equal(sessions?.length, 1)
    assert.equal(sessions?.[0]?.messages[0]?.key, legacyMessage.key)
    assert.equal(memoryStorage.getItem(STORAGE_KEYS.MESSAGES), null)
  })
})
