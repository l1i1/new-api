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

import type { MultiKeyCredentialPayload } from '../types'

/**
 * A key entered in the multi-key editor, optionally with its own proxy.
 *
 * Proxy URLs are deliberately kept out of Channel.key. They are sent through
 * the structured request field so the backend can persist them as credential
 * metadata instead of treating them as another secret.
 */
export type MultiKeyCredentialInput = {
  secret: string
  proxyUrl?: string
}

export type { MultiKeyCredentialPayload } from '../types'

/** JSON credential channels keep their provider-specific key structure. */
export function supportsLineOrientedMultiKeyCredentials(
  type: number,
  vertexKeyType?: 'json' | 'api_key'
): boolean {
  return type !== 57 && !(type === 41 && vertexKeyType !== 'api_key')
}

/** Keep this list in sync with common.ParseProxyURLStrict on the backend. */
const SUPPORTED_PROXY_PROTOCOLS = new Set([
  'http:',
  'https:',
  'socks5:',
  'socks5h:',
])

/**
 * Check the strict, persisted proxy URL syntax used by channel credentials.
 *
 * The URL parser accepts userinfo and IPv6 hosts, while rejecting paths other
 * than the root path, query strings, fragments, invalid ports, and schemes
 * that the runtime cannot use.
 */
export function isStrictProxyURL(value: string | undefined): boolean {
  const trimmedValue = value?.trim() || ''
  if (!trimmedValue) return false

  try {
    const parsedURL = new URL(trimmedValue)
    if (
      !SUPPORTED_PROXY_PROTOCOLS.has(parsedURL.protocol) ||
      !parsedURL.hostname ||
      parsedURL.search ||
      parsedURL.hash ||
      (parsedURL.pathname && parsedURL.pathname !== '/')
    ) {
      return false
    }

    // URL rejects malformed and out-of-range ports. Port zero is rejected by
    // ParseProxyURLStrict as well, even though URL accepts it syntactically.
    return parsedURL.port !== '0'
  } catch {
    return false
  }
}

/**
 * Parse the editor's line-oriented key/proxy syntax.
 *
 * Blank lines are ignored. A valid proxy on the next non-empty line belongs
 * to the preceding key and is consumed; every other non-empty line starts the
 * next key. A proxy-looking first line is therefore retained as a key rather
 * than silently discarded.
 */
export function parseMultiKeyCredentialText(
  value: string | undefined
): MultiKeyCredentialInput[] {
  const lines = String(value || '')
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
  const credentials: MultiKeyCredentialInput[] = []

  for (let index = 0; index < lines.length; index += 1) {
    const secret = lines[index]
    const nextLine = lines[index + 1]
    if (nextLine && isStrictProxyURL(nextLine)) {
      credentials.push({ secret, proxyUrl: nextLine })
      index += 1
      continue
    }
    credentials.push({ secret })
  }

  return credentials
}

/** Convert parser output to the backend request contract. */
export function toMultiKeyCredentialPayload(
  credentials: readonly MultiKeyCredentialInput[]
): MultiKeyCredentialPayload[] {
  return credentials
    .map((credential) => {
      const secret = credential.secret.trim()
      const proxyUrl = credential.proxyUrl?.trim()
      return proxyUrl ? { secret, proxy_url: proxyUrl } : { secret }
    })
    .filter((credential) => credential.secret.length > 0)
}
