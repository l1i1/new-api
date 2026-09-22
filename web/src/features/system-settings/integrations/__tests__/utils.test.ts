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

import { isCompleteHttpUrl, removeTrailingSlash } from '../utils'

describe('integration URL sanitizing', () => {
  test('strips a trailing slash from a real URL', () => {
    expect(removeTrailingSlash('https://api.moonshot.cn/v1/')).toBe(
      'https://api.moonshot.cn/v1'
    )
    expect(removeTrailingSlash('  https://api.moonshot.cn/v1//  ')).toBe(
      'https://api.moonshot.cn/v1'
    )
  })

  test('never turns a bare scheme into a broken one', () => {
    // The saved value would otherwise become "https:/", which no request can
    // be built from — the operator would see a configured setting that never
    // works. Validation rejects the value; sanitizing must not corrupt it into
    // something that still looks plausible.
    expect(removeTrailingSlash('https://')).toBe('https://')
    expect(removeTrailingSlash('http://')).toBe('http://')
  })

  test('treats a blank value as unset', () => {
    expect(removeTrailingSlash('   ')).toBe('')
  })
})

describe('integration URL validation', () => {
  test('accepts a complete http(s) endpoint', () => {
    expect(isCompleteHttpUrl('https://api.moonshot.cn/v1')).toBe(true)
    expect(isCompleteHttpUrl('http://127.0.0.1:8080')).toBe(true)
  })

  test('accepts blank, which the caller reads as unset', () => {
    expect(isCompleteHttpUrl('')).toBe(true)
    expect(isCompleteHttpUrl('   ')).toBe(true)
  })

  test('rejects a scheme with no host', () => {
    // This is the case a prefix check lets through, and it is exactly the one
    // that silently disables the endpoint.
    expect(isCompleteHttpUrl('https://')).toBe(false)
    expect(isCompleteHttpUrl('http://')).toBe(false)
    expect(isCompleteHttpUrl('https:/')).toBe(false)
  })

  test('rejects values that are not absolute URLs', () => {
    expect(isCompleteHttpUrl('api.moonshot.cn/v1')).toBe(false)
    expect(isCompleteHttpUrl('/v1')).toBe(false)
    expect(isCompleteHttpUrl('https://a b.com')).toBe(false)
  })

  test('rejects a non-http scheme', () => {
    expect(isCompleteHttpUrl('ftp://x.com')).toBe(false)
  })
})
