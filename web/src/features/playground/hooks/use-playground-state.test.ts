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
import { describe, test } from 'node:test'

import type { PlaygroundSession } from '../types'
import {
  reorderSessionList,
  type SessionReorderPlacement,
} from './use-playground-state'

function makeSessions(ids: string[]): PlaygroundSession[] {
  return ids.map((id, index) => ({
    id,
    title: id,
    createdAt: index,
    updatedAt: index,
    mode: 'chat',
    config: {
      model: '',
      group: '',
      temperature: 1,
      top_p: 1,
      max_tokens: 2048,
      frequency_penalty: 0,
      presence_penalty: 0,
      seed: null,
      stream: true,
    },
    parameterEnabled: {
      temperature: false,
      top_p: false,
      max_tokens: false,
      frequency_penalty: false,
      presence_penalty: false,
      seed: false,
    },
    messages: [],
  }))
}

const ids = (sessions: PlaygroundSession[]) =>
  sessions.map((session) => session.id)

describe('reorderSessionList', () => {
  test('handles adjacent moves with explicit placement', () => {
    const sessions = makeSessions(['a', 'b', 'c'])

    assert.deepEqual(ids(reorderSessionList(sessions, 'a', 'b', 'before')), [
      'a',
      'b',
      'c',
    ])
    assert.deepEqual(ids(reorderSessionList(sessions, 'a', 'b', 'after')), [
      'b',
      'a',
      'c',
    ])
    assert.deepEqual(ids(reorderSessionList(sessions, 'b', 'a', 'before')), [
      'b',
      'a',
      'c',
    ])
    assert.deepEqual(ids(reorderSessionList(sessions, 'b', 'a', 'after')), [
      'a',
      'b',
      'c',
    ])
  })

  test('handles non-adjacent moves with explicit placement', () => {
    const sessions = makeSessions(['a', 'b', 'c', 'd'])

    assert.deepEqual(ids(reorderSessionList(sessions, 'a', 'c', 'before')), [
      'b',
      'a',
      'c',
      'd',
    ])
    assert.deepEqual(ids(reorderSessionList(sessions, 'a', 'c', 'after')), [
      'b',
      'c',
      'a',
      'd',
    ])
    assert.deepEqual(ids(reorderSessionList(sessions, 'd', 'b', 'before')), [
      'a',
      'd',
      'b',
      'c',
    ])
    assert.deepEqual(ids(reorderSessionList(sessions, 'd', 'b', 'after')), [
      'a',
      'b',
      'd',
      'c',
    ])
    assert.deepEqual(ids(sessions), ['a', 'b', 'c', 'd'])
  })

  test('accepts the placement type used by the reorder callback', () => {
    const placement: SessionReorderPlacement = 'after'
    assert.deepEqual(
      ids(reorderSessionList(makeSessions(['a', 'b']), 'a', 'b', placement)),
      ['b', 'a']
    )
  })
})
