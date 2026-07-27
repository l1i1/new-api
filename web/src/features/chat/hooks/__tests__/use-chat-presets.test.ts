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

import type { ChatPreset } from '../../lib/chat-links'
import { localizeChatPresetLabels } from '../use-chat-presets'

describe('chat preset display localization', () => {
  test('localizes the visible name without changing request data or the source preset', () => {
    const preset: ChatPreset = {
      id: '0',
      name: '<tnt l="zh">聊天</tnt><tnt l="en">Chat</tnt>',
      url: 'https://chat.example/?title=<tnt l="en">raw</tnt>&key={key}',
      type: 'web',
    }
    const sourceSnapshot = structuredClone(preset)

    const localized = localizeChatPresetLabels([preset], 'en-US')

    assert.equal(localized[0]?.name, 'Chat')
    assert.equal(localized[0]?.url, preset.url)
    assert.deepEqual(preset, sourceSnapshot)
  })

  test('uses the active interface language for visible labels', () => {
    const preset: ChatPreset = {
      id: '0',
      name: '<tnt l="zh">聊天</tnt><tnt l="en">Chat</tnt>',
      url: 'https://chat.example/',
      type: 'web',
    }

    assert.equal(localizeChatPresetLabels([preset], 'zh-TW')[0]?.name, '聊天')
    assert.equal(localizeChatPresetLabels([preset], 'fr')[0]?.name, 'Chat')
  })

  test('preserves malformed labels instead of partially changing them', () => {
    const preset: ChatPreset = {
      id: '0',
      name: '<tnt l="en">First</tnt><tnt l="en">Second</tnt>',
      url: 'https://chat.example/',
      type: 'web',
    }

    assert.equal(localizeChatPresetLabels([preset], 'en')[0]?.name, preset.name)
  })
})
