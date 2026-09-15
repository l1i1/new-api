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
  OFFICIAL_FIT_MATCHES,
  normalizeOfficialFitFamilies,
  parseOfficialFit,
  type OfficialFitFamily,
} from '../types'

/** Bypass the response type to feed the normalizer what a bad server would. */
function asFamilies(value: unknown): OfficialFitFamily[] | undefined {
  return value as OfficialFitFamily[] | undefined
}

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

describe('normalizeOfficialFitFamilies', () => {
  test('the backend list is rendered in registry order', () => {
    expect(
      normalizeOfficialFitFamilies([
        { id: 'deepseek-v4', label: 'DeepSeek V4' },
        { id: 'kimi-k3', label: 'Kimi K3' },
      ])
    ).toEqual([
      { match: 'deepseek-v4', label: 'DeepSeek V4' },
      { match: 'kimi-k3', label: 'Kimi K3' },
    ])
  })

  test('a family the frontend never hardcoded still renders', () => {
    const rendered = normalizeOfficialFitFamilies(
      asFamilies([
        ...OFFICIAL_FIT_MATCHES.map((m) => ({
          id: m.match,
          label: m.label,
        })),
        { id: 'qwen-4', label: 'Qwen 4' },
      ])
    )
    expect(rendered).toContainEqual({ match: 'qwen-4', label: 'Qwen 4' })
    expect(rendered).toHaveLength(OFFICIAL_FIT_MATCHES.length + 1)
  })

  test('the fetched list wins over the local fallback', () => {
    expect(
      normalizeOfficialFitFamilies([{ id: 'deepseek-v4', label: 'DeepSeek V4' }])
    ).toHaveLength(1)
  })

  test('a missing label degrades to the id and bad entries are dropped', () => {
    expect(
      normalizeOfficialFitFamilies(
        asFamilies([
          { id: 'glm-5.3' },
          { id: '', label: 'Nameless' },
          { label: 'No id' },
          null,
          'nope',
        ])
      )
    ).toEqual([{ match: 'glm-5.3', label: 'glm-5.3' }])
  })

  test('a non-array or empty body falls back instead of crashing the drawer', () => {
    expect(normalizeOfficialFitFamilies(undefined)).toBe(OFFICIAL_FIT_MATCHES)
    expect(normalizeOfficialFitFamilies(asFamilies(null))).toBe(
      OFFICIAL_FIT_MATCHES
    )
    expect(normalizeOfficialFitFamilies(asFamilies({}))).toBe(
      OFFICIAL_FIT_MATCHES
    )
    expect(normalizeOfficialFitFamilies([])).toBe(OFFICIAL_FIT_MATCHES)
    expect(normalizeOfficialFitFamilies(asFamilies([null, 'x']))).toBe(
      OFFICIAL_FIT_MATCHES
    )
  })
})
