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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { getOptionValue } from './use-system-options'

describe('getOptionValue boolean parsing', () => {
  const cases: Array<[string, boolean]> = [
    ['true', true],
    ['TRUE', true],
    ['t', true],
    ['1', true],
    ['false', false],
    ['FALSE', false],
    ['f', false],
    ['0', false],
  ]

  for (const [value, expected] of cases) {
    test(`parses ${value} as ${expected}`, () => {
      assert.deepEqual(
        getOptionValue([{ key: 'enabled', value }], { enabled: false }),
        { enabled: expected }
      )
    })
  }

  test('falls back to the configured default for invalid values', () => {
    assert.deepEqual(
      getOptionValue([{ key: 'enabled', value: 'sometimes' }], {
        enabled: true,
      }),
      { enabled: true }
    )
  })
})
