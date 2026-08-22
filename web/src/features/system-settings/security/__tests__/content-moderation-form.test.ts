/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

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
  contentModerationSchema,
  toContentModerationFormValues,
  toContentModerationRequest,
} from '../content-moderation-form'

const config = {
  enabled: true,
  mode: 'pre_block' as const,
  base_url: 'https://moderation.example.test',
  model: 'omni-moderation-latest',
  api_key_count: 2,
  api_key_suffixes: ['...1234', '...5678'],
  thresholds: { sexual: 0.65 },
  all_groups: false,
  group_ids: ['pro', 'safe'],
  all_models: false,
  models: ['gpt-4o'],
  model_filters: ['claude-*'],
  sample_rate: 0.5,
  timeout_ms: 1500,
  retry_count: 1,
  max_in_flight_per_key: 1,
  queue_wait_ms: 200,
  overload_status: 503 as const,
  key_cooldown_ms: 5000,
  record_non_hits: false,
  block_status: 451,
  block_message: 'Blocked',
  email_on_hit: true,
  auto_ban_enabled: true,
  ban_threshold: 10,
  violation_window_hours: 24,
}

describe('content moderation form mapping', () => {
  test('does not send an empty API key when editing an existing config', () => {
    const values = toContentModerationFormValues(config)
    const request = toContentModerationRequest(values)

    expect(request.api_key).toBeUndefined()
    expect(request.group_ids).toEqual(['pro', 'safe'])
    expect(request.model_filters).toEqual(['claude-*'])
    expect(request.sample_rate).toBe(0.5)
    expect(request.max_in_flight_per_key).toBe(1)
    expect(request.queue_wait_ms).toBe(200)
    expect(request.overload_status).toBe(503)
    expect(request.key_cooldown_ms).toBe(5000)
    expect(request).not.toHaveProperty('record_logs')
  })

  test('sends a newly entered multiline API key list', () => {
    const values = toContentModerationFormValues(config)
    values.api_key = 'first\nsecond'

    expect(toContentModerationRequest(values).api_key).toBe('first\nsecond')
  })

  test('sends an explicit flag when stored API keys are cleared', () => {
    const values = toContentModerationFormValues(config)
    values.clear_api_keys = true

    const request = toContentModerationRequest(values)
    expect(request.api_key).toBeUndefined()
    expect(request.clear_api_keys).toBe(true)
  })

  test('rejects thresholds outside the inclusive zero-to-one range', () => {
    expect(
      contentModerationSchema.safeParse({
        ...toContentModerationFormValues(config),
        thresholds: '{"sexual": 1.1}',
      }).success
    ).toBe(false)
  })

  test('rejects violation windows longer than one year', () => {
    expect(
      contentModerationSchema.safeParse({
        ...toContentModerationFormValues(config),
        violation_window_hours: '8761',
      }).success
    ).toBe(false)
  })

  test('accepts max in-flight values above the legacy limit', () => {
    const values = {
      ...toContentModerationFormValues(config),
      max_in_flight_per_key: '128',
    }

    expect(contentModerationSchema.safeParse(values).success).toBe(true)
    expect(toContentModerationRequest(values).max_in_flight_per_key).toBe(128)
  })

  test('rejects unsupported overload statuses', () => {
    expect(
      contentModerationSchema.safeParse({
        ...toContentModerationFormValues(config),
        overload_status: '403',
      }).success
    ).toBe(false)
  })
})
