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

import type { NavGroup } from '@/components/layout/types'

import {
  applySidebarNavigationConfig,
  isInvoiceFeatureEnabled,
} from '../use-sidebar-config'

describe('sidebar invoice feature gating', () => {
  test('accepts the boolean status value exposed by /api/status', () => {
    assert.equal(isInvoiceFeatureEnabled({ invoice_enabled: true }), true)
    assert.equal(isInvoiceFeatureEnabled({ invoice_enabled: false }), false)
  })

  test('fails closed when the status value is absent or malformed', () => {
    assert.equal(isInvoiceFeatureEnabled(null), false)
    assert.equal(isInvoiceFeatureEnabled({}), false)
    assert.equal(isInvoiceFeatureEnabled({ invoice_enabled: 'false' }), false)
    assert.equal(isInvoiceFeatureEnabled({ invoice_enabled: 'TRUE' }), true)
    assert.equal(isInvoiceFeatureEnabled({ invoice_enabled: 1 }), true)
    assert.equal(isInvoiceFeatureEnabled({ invoice_enabled: 0 }), false)
    assert.equal(isInvoiceFeatureEnabled({ invoice_enabled: 'enabled' }), false)
  })

  test('supports the legacy option casing for cached status payloads', () => {
    assert.equal(isInvoiceFeatureEnabled({ InvoiceEnabled: true }), true)
  })

  test('hides both invoice entries when the backend feature is disabled', () => {
    const groups: NavGroup[] = [
      {
        id: 'personal',
        title: 'Personal',
        items: [
          { title: 'Wallet', url: '/wallet' },
          { title: 'Invoices', url: '/invoice' },
        ],
      },
      {
        id: 'admin',
        title: 'Admin',
        items: [
          { title: 'Invoice Review', url: '/invoices' },
          { title: 'Users', url: '/users' },
        ],
      },
    ]
    const config = {
      personal: { enabled: true, topup: true, invoice: true },
      admin: { enabled: true, invoice_admin: true, user: true },
    }

    const result = applySidebarNavigationConfig(groups, config, null, false)

    assert.deepEqual(
      result.flatMap((group) => group.items).map((item) => item.title),
      ['Wallet', 'Users']
    )
  })

  test('orders sidebar sections and entries using the saved configuration', () => {
    const groups: NavGroup[] = [
      {
        id: 'general',
        title: 'General',
        items: [
          { title: 'Overview', url: '/dashboard/overview' },
          { title: 'API Keys', url: '/keys' },
        ],
      },
      {
        id: 'personal',
        title: 'Personal',
        items: [
          { title: 'Wallet', url: '/wallet' },
          { title: 'Profile', url: '/profile' },
        ],
      },
    ]
    const config = {
      personal: { enabled: true, personal: true, topup: true },
      console: { enabled: true, token: true, detail: true },
    }

    const result = applySidebarNavigationConfig(groups, config, null, true)

    assert.deepEqual(
      result.map((group) => group.title),
      ['Personal', 'General']
    )
    assert.deepEqual(
      result.map((group) => group.items.map((item) => item.title)),
      [
        ['Profile', 'Wallet'],
        ['API Keys', 'Overview'],
      ]
    )
  })

  test('uses the earliest configured module position for merged task logs', () => {
    const groups: NavGroup[] = [
      {
        id: 'general',
        title: 'General',
        items: [
          { title: 'Overview', url: '/dashboard/overview' },
          { title: 'Usage Logs', url: '/usage-logs/common' },
          {
            title: 'Task Logs',
            url: '/usage-logs/task',
            activeUrls: ['/usage-logs/drawing'],
            configUrls: ['/usage-logs/drawing', '/usage-logs/task'],
          },
        ],
      },
    ]
    const config = {
      console: {
        enabled: true,
        midjourney: true,
        detail: true,
        token: true,
        log: true,
        task: true,
      },
    }

    const result = applySidebarNavigationConfig(groups, config, null, true)

    assert.deepEqual(
      result[0]?.items.map((item) => item.title),
      ['Task Logs', 'Overview', 'Usage Logs']
    )
  })
})
