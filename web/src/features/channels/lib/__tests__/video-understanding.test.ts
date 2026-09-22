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
import { describe, expect, test } from 'vitest'

import {
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
} from '../channel-form'
import type { Channel } from '../../types'

function settingsFrom(
  form: Parameters<typeof transformFormDataToCreatePayload>[0]
) {
  const { channel } = transformFormDataToCreatePayload(form)
  return JSON.parse(String(channel.settings))
}

function baseChannel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: 15,
    type: 1,
    name: 'DEF_zz-1',
    key: '',
    base_url: '',
    models: 'kimi-k3',
    group: 'default',
    status: 1,
    priority: 0,
    weight: 0,
    settings: '{}',
    channel_info: {
      is_multi_key: false,
      multi_key_size: 0,
      multi_key_polling_index: 0,
      multi_key_mode: 'random',
    },
    ...overrides,
  } as Channel
}

describe('video understanding channel settings', () => {
  test('loads both marks out of the settings JSON', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({
          supports_video: true,
          video_usage_mode: 'estimate',
        }),
      })
    )
    expect(form.supports_video).toBe(true)
    expect(form.video_usage_mode).toBe('estimate')
  })

  test('a capability-only channel loads with the trusting mode', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({ settings: JSON.stringify({ supports_video: true }) })
    )
    expect(form.supports_video).toBe(true)
    expect(form.video_usage_mode).toBe('')
  })

  test('defaults to unmarked when the settings are absent', () => {
    const form = transformChannelToFormDefaults(baseChannel())
    expect(form.supports_video).toBe(false)
    expect(form.video_usage_mode).toBe('')
  })

  test('an unrecognised mode reads as trusting, not as some other rule', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({
          supports_video: true,
          video_usage_mode: 'soemthing-else',
        }),
      })
    )
    expect(form.video_usage_mode).toBe('')
  })

  test('clearing the capability removes both keys', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({
          supports_video: true,
          video_usage_mode: 'estimate',
        }),
      })
    )
    form.supports_video = false
    const settings = settingsFrom(form)
    expect(settings).not.toHaveProperty('supports_video')
    expect(settings).not.toHaveProperty('video_usage_mode')
  })

  test('a stale estimate mode cannot survive the capability being cleared', () => {
    // The server rejects `video_usage_mode` without `supports_video`, so leaving
    // the mode behind would fail the save with an error naming a field the
    // operator can no longer see.
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({
          supports_video: true,
          video_usage_mode: 'estimate',
        }),
      })
    )
    form.supports_video = false
    form.video_usage_mode = 'estimate'
    const settings = settingsFrom(form)
    expect(settings).not.toHaveProperty('video_usage_mode')
  })

  test('the mode is dropped when it is switched back to trusting', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({
          supports_video: true,
          video_usage_mode: 'estimate',
        }),
      })
    )
    form.video_usage_mode = ''
    const settings = settingsFrom(form)
    expect(settings.supports_video).toBe(true)
    expect(settings).not.toHaveProperty('video_usage_mode')
  })

  test('preserves unrelated settings across the round trip', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({
          disable_task_polling_sleep: true,
          supports_video: true,
          video_usage_mode: 'estimate',
        }),
      })
    )
    const settings = settingsFrom(form)
    expect(settings.disable_task_polling_sleep).toBe(true)
    expect(settings.supports_video).toBe(true)
    expect(settings.video_usage_mode).toBe('estimate')
  })
})
