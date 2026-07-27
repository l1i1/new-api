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
  filterModelGroupOptions,
  localizeModelGroupDescriptions,
  modelGroupMatchesSearch,
  type GroupOption,
} from '../group-localization'

describe('model group description localization', () => {
  test('localizes desc and description while preserving identifiers and source data', () => {
    const groups: GroupOption[] = [
      {
        value: 'standard',
        label: 'Standard',
        desc: '<tnt l="zh">标准组</tnt><tnt l="en">Standard group</tnt>',
        ratio: 1,
      },
      {
        value: 'premium',
        label: 'Premium',
        description:
          '<tnt l="zh">专业组</tnt><tnt l="en">Professional group</tnt>',
        ratio: 2,
      },
    ]
    const sourceSnapshot = structuredClone(groups)

    const localized = localizeModelGroupDescriptions(groups, 'en-US')

    assert.equal(localized[0]?.desc, 'Standard group')
    assert.equal(localized[1]?.description, 'Professional group')
    assert.deepEqual(
      localized.map((group) => [group.value, group.label, group.ratio]),
      [
        ['standard', 'Standard', 1],
        ['premium', 'Premium', 2],
      ]
    )
    assert.deepEqual(groups, sourceSnapshot)
  })

  test('matches search against the localized desc and description values', () => {
    const localized = localizeModelGroupDescriptions(
      [
        {
          value: 'standard',
          label: 'Standard',
          desc: '<tnt l="zh">标准组</tnt><tnt l="en">Standard group</tnt>',
        },
        {
          value: 'premium',
          label: 'Premium',
          description:
            '<tnt l="zh">专业组</tnt><tnt l="en">Professional group</tnt>',
        },
      ],
      'zh'
    )
    const standardGroup = localized[0]
    const premiumGroup = localized[1]

    assert.ok(standardGroup)
    assert.ok(premiumGroup)
    assert.equal(modelGroupMatchesSearch(standardGroup, '标准'), true)
    assert.equal(modelGroupMatchesSearch(premiumGroup, '专业'), true)
    assert.equal(modelGroupMatchesSearch(premiumGroup, 'professional'), false)
  })

  test('filters the Playground group list by the current-language description', () => {
    const localized = localizeModelGroupDescriptions(
      [
        {
          value: 'standard',
          label: 'Standard',
          desc: '<tnt l="zh">标准组</tnt><tnt l="en">Standard group</tnt>',
        },
        {
          value: 'premium',
          label: 'Premium',
          desc: '<tnt l="zh">专业组</tnt><tnt l="en">Professional group</tnt>',
        },
      ],
      'zh'
    )

    assert.deepEqual(
      filterModelGroupOptions(localized, '专业').map((group) => group.value),
      ['premium']
    )
    assert.deepEqual(filterModelGroupOptions(localized, 'professional'), [])
  })
})
