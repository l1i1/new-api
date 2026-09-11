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

function settingsFrom(form: Parameters<typeof transformFormDataToCreatePayload>[0]) {
  const { channel } = transformFormDataToCreatePayload(form)
  return JSON.parse(String(channel.settings))
}

function baseChannel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: 12,
    type: 1,
    name: 'DEF_langlangya',
    key: '',
    base_url: '',
    models: 'deepseek-v4.1-flash',
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

describe('official_fit_models channel setting', () => {
  test('loads the allowlist from settings JSON into the form', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({
          official_fit_models: ['deepseek-v4.1-flash', 'deepseek-v4-flash'],
        }),
      })
    )
    expect(form.official_fit_models).toBe(
      'deepseek-v4.1-flash,deepseek-v4-flash'
    )
  })

  test('defaults to empty when the setting is absent', () => {
    const form = transformChannelToFormDefaults(baseChannel())
    expect(form.official_fit_models).toBe('')
  })

  test('serializes a comma list back to a de-duplicated JSON array', () => {
    const form = transformChannelToFormDefaults(baseChannel())
    form.official_fit_models =
      'deepseek-v4.1-flash, deepseek-v4-flash,deepseek-v4.1-flash'
    const settings = settingsFrom(form)
    expect(settings.official_fit_models).toEqual([
      'deepseek-v4.1-flash',
      'deepseek-v4-flash',
    ])
  })

  test('drops the key when the field is cleared', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({ official_fit_models: ['deepseek-v4.1-flash'] }),
      })
    )
    form.official_fit_models = '   '
    const settings = settingsFrom(form)
    expect(settings).not.toHaveProperty('official_fit_models')
  })

  test('preserves unrelated settings across the round trip', () => {
    const form = transformChannelToFormDefaults(
      baseChannel({
        settings: JSON.stringify({
          disable_task_polling_sleep: true,
          official_fit_models: ['deepseek-v4.1-flash'],
        }),
      })
    )
    const settings = settingsFrom(form)
    expect(settings.disable_task_polling_sleep).toBe(true)
    expect(settings.official_fit_models).toEqual(['deepseek-v4.1-flash'])
  })
})
