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

import type { Channel } from '../../types'
import {
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
} from '../channel-form'

function settingFrom(
  form: Parameters<typeof transformFormDataToCreatePayload>[0]
) {
  const { channel } = transformFormDataToCreatePayload(form)
  return JSON.parse(String(channel.setting))
}

function baseChannel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: 21,
    type: 1,
    name: 'concurrency-channel',
    key: '',
    base_url: '',
    models: 'test-model',
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

describe('concurrency_limit channel setting', () => {
  test('loads the limit from setting JSON into the form', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({ setting: JSON.stringify({ concurrency_limit: 5 }) })
    )
    expect(form.concurrency_limit).toBe(5)
  })

  test('defaults to 0 (unlimited) when the setting is absent', () => {
    const form = transformChannelToFormDefaults(baseChannel())
    expect(form.concurrency_limit).toBe(0)
  })

  test('serializes a positive limit into the setting JSON', () => {
    const form = transformChannelToFormDefaults(baseChannel())
    form.concurrency_limit = 8
    const settings = settingFrom(form)
    expect(settings.concurrency_limit).toBe(8)
  })

  test('drops the key when the limit is cleared back to unlimited', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({ setting: JSON.stringify({ concurrency_limit: 5 }) })
    )
    form.concurrency_limit = 0
    const settings = settingFrom(form)
    expect(settings).not.toHaveProperty('concurrency_limit')
  })

  test('preserves unrelated settings across the round trip', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        setting: JSON.stringify({
          force_format: true,
          concurrency_limit: 3,
        }),
      })
    )
    const settings = settingFrom(form)
    expect(settings.force_format).toBe(true)
    expect(settings.concurrency_limit).toBe(3)
  })
})
