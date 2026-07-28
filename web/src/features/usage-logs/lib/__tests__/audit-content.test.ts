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

import { renderAuditContent } from '../format'

describe('channel audit content', () => {
  test('renders a used-quota reset with its previous value', () => {
    const rendered = renderAuditContent(
      {
        op: {
          action: 'channel.used_quota_reset',
          params: {
            id: 93,
            name: 'OpenCode Go',
            previous_used_quota: 9876,
          },
        },
      },
      (template, params) =>
        template.replaceAll(/{{(\w+)}}/g, (_match, key: string) =>
          String(params?.[key] ?? '')
        )
    )

    assert.equal(
      rendered,
      'Reset used quota for channel OpenCode Go (ID: 93) from 9876 to 0'
    )
  })
})
