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
import { describe, expect, it } from 'vitest'

import {
  EMPTY_OFFICIAL_FIT_ADMISSION,
  allOfficialFamiliesMeasured,
  filterIgnoredOfficialFitModels,
  isIgnoredOfficialFitModel,
  measuredFamilyPrefixes,
} from '../official-fit-admission'

const kimiMeasured = {
  official_model_prefixes: ['deepseek-v4', 'kimi-k3', 'glm-5.3'],
  measured_model_prefixes: ['kimi-k3'],
}

describe('isIgnoredOfficialFitModel', () => {
  it('matches by case-insensitive prefix', () => {
    expect(isIgnoredOfficialFitModel('kimi-k3', ['kimi-k3'])).toBe(true)
    expect(isIgnoredOfficialFitModel(' Kimi-K3-Turbo ', ['kimi-k3'])).toBe(
      true
    )
    expect(isIgnoredOfficialFitModel('deepseek-v4.1-flash', ['kimi-k3'])).toBe(
      false
    )
  })

  it('treats empty input as not ignored', () => {
    expect(isIgnoredOfficialFitModel('  ', ['kimi-k3'])).toBe(false)
  })
})

describe('filterIgnoredOfficialFitModels', () => {
  it('drops ignored entries and keeps the rest trimmed', () => {
    expect(
      filterIgnoredOfficialFitModels(
        ' kimi-k3 , deepseek-v4.1-flash ,  ',
        ['kimi-k3']
      )
    ).toBe('deepseek-v4.1-flash')
  })

  it('returns the value untouched without measured prefixes', () => {
    expect(filterIgnoredOfficialFitModels('kimi-k3', [])).toBe('kimi-k3')
    expect(filterIgnoredOfficialFitModels('', ['kimi-k3'])).toBe('')
  })

  it('empties a value made only of ignored entries', () => {
    expect(filterIgnoredOfficialFitModels('kimi-k3, kimi-k3-turbo', ['kimi-k3'])).toBe('')
  })
})

describe('allOfficialFamiliesMeasured', () => {
  it('is false while any family is declared', () => {
    expect(allOfficialFamiliesMeasured(kimiMeasured)).toBe(false)
  })

  it('is true when every official prefix is measured', () => {
    expect(
      allOfficialFamiliesMeasured({
        official_model_prefixes: ['deepseek-v4', 'kimi-k3', 'glm-5.3'],
        measured_model_prefixes: ['kimi-k3', 'deepseek-v4', 'glm-5.3'],
      })
    ).toBe(true)
  })

  it('is false for the empty (no snapshot) state', () => {
    expect(allOfficialFamiliesMeasured(EMPTY_OFFICIAL_FIT_ADMISSION)).toBe(
      false
    )
  })
})

describe('measuredFamilyPrefixes', () => {
  it('names the measured families while some family is still declared', () => {
    expect(measuredFamilyPrefixes(kimiMeasured)).toEqual(['kimi-k3'])
  })

  it('is empty once every family is measured (the input is hidden then)', () => {
    expect(
      measuredFamilyPrefixes({
        official_model_prefixes: ['kimi-k3'],
        measured_model_prefixes: ['kimi-k3'],
      })
    ).toEqual([])
  })
})
