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

import type { PricingModel } from '../../types'
import {
  extractAllTags,
  filterBySearch,
  filterByTag,
  filterByVendor,
} from '../filters'

const vendorName = '阿里巴巴'
const model: PricingModel = {
  id: 1,
  model_name: 'qwen-test',
  vendor_id: 1,
  vendor_name: vendorName,
  vendor_display_name: '<tnt l="zh">阿里巴巴</tnt><tnt l="en">Alibaba</tnt>',
  vendor_localized_name: 'Alibaba',
  tags: '<tnt l="zh">免费</tnt><tnt l="en">Free</tnt>',
  localized_tags: 'Free',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
}

describe('pricing vendor filtering', () => {
  test('finds a model by its visible localized vendor name', () => {
    assert.deepEqual(filterBySearch([model], 'Alibaba'), [model])
  })

  test('does not search hidden translations or localization markup', () => {
    const chineseModel = { ...model, vendor_localized_name: vendorName }

    assert.deepEqual(filterBySearch([chineseModel], 'Alibaba'), [])
    assert.deepEqual(filterBySearch([chineseModel], '<tnt'), [])
    assert.deepEqual(filterBySearch([chineseModel], 'l="en"'), [])
  })

  test('uses only localized model tags for search and tag filtering', () => {
    assert.deepEqual(filterBySearch([model], 'Free'), [model])
    assert.deepEqual(filterBySearch([model], '免费'), [])
    assert.deepEqual(filterBySearch([model], '<tnt'), [])
    assert.deepEqual(filterByTag([model], 'free'), [model])
    assert.deepEqual(filterByTag([model], '免费'), [])
    assert.deepEqual(extractAllTags([model]), ['free'])
  })

  test('keeps vendor filtering bound to the canonical name', () => {
    assert.deepEqual(filterByVendor([model], vendorName), [model])
    assert.deepEqual(filterByVendor([model], 'Alibaba'), [])
  })
})
