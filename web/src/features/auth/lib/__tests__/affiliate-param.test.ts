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
import { beforeEach, describe, expect, test } from 'vitest'

import {
  AFF_CODE_PARAM,
  PARTNER_CODE_PARAM,
  captureAffiliateCode,
  readAffiliateCode,
  urlWithAffiliateCode,
} from '../affiliate-param'
import { clearAffiliateCode, getAffiliateCode } from '../storage'

beforeEach(() => {
  window.localStorage.clear()
})

describe('readAffiliateCode', () => {
  test('reads the partner parameter', () => {
    expect(readAffiliateCode('?paff=TOMMY-A1')).toBe('TOMMY-A1')
  })

  test('keeps the site’s own aff parameter working', () => {
    expect(readAffiliateCode('?aff=O30E')).toBe('O30E')
  })

  test('prefers the partner parameter when both are present', () => {
    expect(readAffiliateCode('?aff=O30E&paff=TOMMY-A1')).toBe('TOMMY-A1')
  })

  test('ignores empty and unrelated parameters', () => {
    expect(readAffiliateCode('?paff=%20%20')).toBe('')
    expect(readAffiliateCode('?redirect=/dashboard')).toBe('')
    expect(readAffiliateCode('')).toBe('')
  })
})

describe('captureAffiliateCode', () => {
  test('persists what the landing page carried', () => {
    expect(captureAffiliateCode('?paff=TOMMY-A1')).toBe('TOMMY-A1')
    expect(getAffiliateCode()).toBe('TOMMY-A1')
  })

  test('falls back to the stored code on a page that carries none', () => {
    captureAffiliateCode('?paff=TOMMY-A1')
    expect(captureAffiliateCode('?redirect=/pricing')).toBe('TOMMY-A1')
  })

  test('a newer link replaces the stored code', () => {
    captureAffiliateCode('?paff=TOMMY-A1')
    expect(captureAffiliateCode('?paff=TOMMY-A2')).toBe('TOMMY-A2')
    expect(getAffiliateCode()).toBe('TOMMY-A2')
  })

  test('the newest link wins across parameter names', () => {
    // A visitor who first followed a partner link and later a friend's
    // referral link belongs to the friend: the last link they followed is the
    // one that brought them in.
    captureAffiliateCode('?paff=PARTNER-A')
    expect(captureAffiliateCode('?aff=FRIEND')).toBe('FRIEND')
    expect(getAffiliateCode()).toBe('FRIEND')
  })

  test('nothing is captured without a code', () => {
    expect(captureAffiliateCode('?redirect=/pricing')).toBe('')
    expect(getAffiliateCode()).toBe('')
  })

  test('clearing ends the capture, as a completed registration does', () => {
    captureAffiliateCode('?paff=TOMMY-A1')
    clearAffiliateCode()
    expect(getAffiliateCode()).toBe('')
  })
})

describe('urlWithAffiliateCode', () => {
  test('adds the code to a bare URL', () => {
    expect(urlWithAffiliateCode('https://tokeness.ai/pricing', 'TOMMY-A1')).toBe(
      'https://tokeness.ai/pricing?paff=TOMMY-A1'
    )
  })

  test('keeps existing query parameters and the hash', () => {
    expect(
      urlWithAffiliateCode(
        'https://tokeness.ai/pricing?model=foo#plans',
        'TOMMY-A1'
      )
    ).toBe('https://tokeness.ai/pricing?model=foo&paff=TOMMY-A1#plans')
  })

  test('leaves a URL that already carries a code alone, either spelling', () => {
    expect(
      urlWithAffiliateCode('https://tokeness.ai/?paff=OLD', 'TOMMY-A1')
    ).toBe('')
    expect(urlWithAffiliateCode('https://tokeness.ai/?aff=O30E', 'TOMMY-A1')).toBe(
      ''
    )
  })

  test('does nothing without a code', () => {
    expect(urlWithAffiliateCode('https://tokeness.ai/pricing', '')).toBe('')
  })

  test('the parameter names stay the ones the site reads', () => {
    expect(PARTNER_CODE_PARAM).toBe('paff')
    expect(AFF_CODE_PARAM).toBe('aff')
  })
})
