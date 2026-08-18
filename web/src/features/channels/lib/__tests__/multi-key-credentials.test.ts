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
  isStrictProxyURL,
  parseMultiKeyCredentialText,
  supportsLineOrientedMultiKeyCredentials,
  toMultiKeyCredentialPayload,
} from '../multi-key-credentials'

describe('multi-key credential text', () => {
  test('associates a strict proxy on the next non-empty line with its key', () => {
    const credentials = parseMultiKeyCredentialText(
      'key-one\n\nhttps://user:pass@proxy.example:8080\nkey-two\nnot-a-proxy\nkey-three\nsocks5h://proxy.example'
    )

    assert.deepEqual(credentials, [
      {
        secret: 'key-one',
        proxyUrl: 'https://user:pass@proxy.example:8080',
      },
      { secret: 'key-two' },
      { secret: 'not-a-proxy' },
      { secret: 'key-three', proxyUrl: 'socks5h://proxy.example' },
    ])
  })

  test('treats an invalid proxy line as the next key', () => {
    assert.deepEqual(
      parseMultiKeyCredentialText('key-one\nsocks4://proxy.example\nkey-two'),
      [
        { secret: 'key-one' },
        { secret: 'socks4://proxy.example' },
        { secret: 'key-two' },
      ]
    )
  })

  test('uses the backend strict proxy protocol and suffix rules', () => {
    assert.equal(isStrictProxyURL('http://proxy.example'), true)
    assert.equal(isStrictProxyURL('socks5://proxy.example'), true)
    assert.equal(isStrictProxyURL('socks5h://proxy.example:1080/'), true)
    assert.equal(isStrictProxyURL('socks4://proxy.example'), false)
    assert.equal(isStrictProxyURL('http://proxy.example/path'), false)
    assert.equal(isStrictProxyURL('http://proxy.example?token=1'), false)
    assert.equal(isStrictProxyURL('http://proxy.example?'), false)
    assert.equal(isStrictProxyURL('http://proxy.example#'), false)
    assert.equal(isStrictProxyURL('http://proxy.example/?'), false)
    assert.equal(isStrictProxyURL('http://proxy.example/#'), false)
    assert.equal(isStrictProxyURL('http://proxy.example:0'), false)
  })

  test('keeps strict proxy rejections as subsequent keys', () => {
    assert.deepEqual(
      parseMultiKeyCredentialText('key-one\nhttp://proxy.example?\nkey-two'),
      [
        { secret: 'key-one' },
        { secret: 'http://proxy.example?' },
        { secret: 'key-two' },
      ]
    )
  })

  test('keeps JSON credential channels outside the line-oriented parser', () => {
    assert.equal(supportsLineOrientedMultiKeyCredentials(1), true)
    assert.equal(supportsLineOrientedMultiKeyCredentials(41, 'json'), false)
    assert.equal(supportsLineOrientedMultiKeyCredentials(41, 'api_key'), true)
    assert.equal(supportsLineOrientedMultiKeyCredentials(57), false)
  })

  test('maps proxy metadata to the structured backend field', () => {
    assert.deepEqual(
      toMultiKeyCredentialPayload([
        { secret: ' key-one ', proxyUrl: ' http://proxy.example ' },
        { secret: 'key-two' },
      ]),
      [
        { secret: 'key-one', proxy_url: 'http://proxy.example' },
        { secret: 'key-two' },
      ]
    )
  })
})
