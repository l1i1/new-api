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

import { moveSidebarItem, parseSidebarModulesAdmin } from '../config'

describe('sidebar module configuration ordering', () => {
  test('moves sections and modules without mutating the source order', () => {
    const items = ['chat', 'console', 'personal', 'admin']

    assert.deepEqual(moveSidebarItem(items, 'personal', -1), [
      'chat',
      'personal',
      'console',
      'admin',
    ])
    assert.deepEqual(moveSidebarItem(items, 'chat', -1), items)
    assert.deepEqual(items, ['chat', 'console', 'personal', 'admin'])
  })

  test('preserves configured module order while appending newly introduced defaults', () => {
    const parsed = parseSidebarModulesAdmin(
      JSON.stringify({
        personal: {
          enabled: true,
          personal: true,
          topup: true,
        },
      })
    )

    assert.deepEqual(Object.keys(parsed.personal), [
      'enabled',
      'personal',
      'topup',
      'invoice',
    ])
  })

  test('includes System Info as an administrator module', () => {
    const parsed = parseSidebarModulesAdmin('')

    assert.equal(parsed.admin.system_info, true)
  })
})
