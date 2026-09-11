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

import { OFFICIAL_FIT_MATCHES, parseOfficialFit } from '../types'

describe('parseOfficialFit family-key migration', () => {
  test('the DeepSeek family key is dash-free so v4.1 is covered', () => {
    expect(
      OFFICIAL_FIT_MATCHES.find((m) => m.match.startsWith('deepseek'))
    ).toEqual({ match: 'deepseek-v4', label: 'DeepSeek V4' })
  })

  test('a legacy deepseek-v4- profile is surfaced under the new key', () => {
    const setting = JSON.stringify({
      official_fit: {
        profile: {
          'deepseek-v4-': { validate: true, route: true },
          'kimi-k3': { shape: true },
        },
      },
    })
    const config = parseOfficialFit(setting)
    expect(config.profile).toEqual({
      'deepseek-v4': { validate: true, route: true },
      'kimi-k3': { shape: true },
    })
  })

  test('the canonical key wins field-by-field over the legacy entry', () => {
    const setting = JSON.stringify({
      official_fit: {
        profile: {
          'deepseek-v4-': { validate: true, errors: true },
          'deepseek-v4': { route: true },
        },
      },
    })
    const config = parseOfficialFit(setting)
    expect(config.profile).toEqual({
      'deepseek-v4': { validate: true, errors: true, route: true },
    })
  })

  test('unrelated profiles and bad input are left alone', () => {
    const config = parseOfficialFit(
      JSON.stringify({ official_fit: { profile: { 'glm-5.3': { shape: true } } } })
    )
    expect(config.profile).toEqual({ 'glm-5.3': { shape: true } })
    expect(parseOfficialFit('not-json')).toEqual({})
    expect(parseOfficialFit(undefined)).toEqual({})
  })
})
