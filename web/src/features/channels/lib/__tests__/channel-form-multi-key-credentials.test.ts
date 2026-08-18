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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
} from '../channel-form'

describe('channel form multi-key credential payloads', () => {
  test('sends only secrets in channel.key and proxy metadata structurally on create', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'Proxy pool',
      models: 'gpt-5',
      key: 'key-one\nhttp://proxy.example:8080\nkey-two',
      multi_key_mode: 'multi_to_single',
    })

    assert.equal(payload.channel.key, 'key-one\nkey-two')
    assert.deepEqual(payload.multi_key_credentials, [
      { secret: 'key-one', proxy_url: 'http://proxy.example:8080' },
      { secret: 'key-two' },
    ])
  })

  test('sends structured credentials on existing multi-key updates', () => {
    const payload = transformFormDataToUpdatePayload(
      {
        ...CHANNEL_FORM_DEFAULT_VALUES,
        type: 1,
        models: 'gpt-5',
        key: 'key-one\nsocks5://proxy.example\nkey-two',
        vertex_key_type: 'json',
      },
      42,
      { isMultiKeyChannel: true }
    )

    assert.equal(payload.key, 'key-one\nkey-two')
    assert.deepEqual(payload.multi_key_credentials, [
      { secret: 'key-one', proxy_url: 'socks5://proxy.example' },
      { secret: 'key-two' },
    ])
  })

  test('keeps ordinary single and batch payloads unchanged', () => {
    const single = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'Single',
      models: 'gpt-5',
      key: 'single-key',
      multi_key_mode: 'single',
    })
    const batch = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'Batch',
      models: 'gpt-5',
      key: 'key-one\nkey-two',
      multi_key_mode: 'batch',
    })

    assert.equal(single.channel.key, 'single-key')
    assert.equal(single.multi_key_credentials, undefined)
    assert.equal(batch.channel.key, 'key-one\nkey-two')
    assert.equal(batch.multi_key_credentials, undefined)
  })

  test('keeps Vertex JSON credentials untouched even in a multi-key mode value', () => {
    const key = '[{"type":"service_account","project_id":"demo"}]'
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: 41,
      vertex_key_type: 'json',
      name: 'Vertex',
      models: 'gemini-2.5-pro',
      key,
      multi_key_mode: 'multi_to_single',
    })

    assert.equal(payload.channel.key, key)
    assert.equal(payload.multi_key_credentials, undefined)
  })

  test('keeps append mode for legacy JSON multi-key updates', () => {
    const key = '[{"type":"service_account","project_id":"demo"}]'
    const payload = transformFormDataToUpdatePayload(
      {
        ...CHANNEL_FORM_DEFAULT_VALUES,
        type: 41,
        vertex_key_type: 'json',
        models: 'gemini-2.5-pro',
        key,
        key_mode: 'append',
      },
      42,
      { isMultiKeyChannel: true }
    )

    assert.equal(payload.key, key)
    assert.equal(payload.key_mode, 'append')
    assert.equal(payload.multi_key_credentials, undefined)
  })

  test('supports line-oriented Vertex API-key multi-key creation', () => {
    const payload = transformFormDataToCreatePayload({
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: 41,
      vertex_key_type: 'api_key',
      name: 'Vertex API keys',
      models: 'gemini-2.5-pro',
      key: 'key-one\nhttp://proxy.example:8080\nkey-two',
      multi_key_mode: 'multi_to_single',
    })

    assert.equal(payload.channel.key, 'key-one\nkey-two')
    assert.deepEqual(payload.multi_key_credentials, [
      { secret: 'key-one', proxy_url: 'http://proxy.example:8080' },
      { secret: 'key-two' },
    ])
  })
})
