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

import {
  filterApiKeyGroupOptions,
  localizeApiKeyGroupDescriptions,
  type ApiKeyGroupOption,
} from '../api-key-group-options'

describe('API key group description localization', () => {
  test('localizes only descriptions without mutating group values or source data', () => {
    const options: ApiKeyGroupOption[] = [
      {
        value: 'premium',
        label: 'Premium',
        desc: '<tnt l="zh">专业组</tnt><tnt l="en">Professional group</tnt>',
        ratio: 2,
      },
    ]
    const sourceSnapshot = structuredClone(options)

    const localized = localizeApiKeyGroupDescriptions(options, 'zh-TW')

    assert.equal(localized[0]?.desc, '专业组')
    assert.equal(localized[0]?.value, 'premium')
    assert.equal(localized[0]?.label, 'Premium')
    assert.equal(localized[0]?.ratio, 2)
    assert.deepEqual(options, sourceSnapshot)
  })

  test('searches the localized description instead of hidden translations', () => {
    const localized = localizeApiKeyGroupDescriptions(
      [
        {
          value: 'premium',
          label: 'Premium',
          desc: '<tnt l="zh">专业组</tnt><tnt l="en">Professional group</tnt>',
        },
      ],
      'zh'
    )

    assert.equal(filterApiKeyGroupOptions(localized, '专业').length, 1)
    assert.equal(filterApiKeyGroupOptions(localized, 'professional').length, 0)
  })
})
