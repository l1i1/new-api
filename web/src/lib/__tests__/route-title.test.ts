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
import { describe, expect, test, vi } from 'vitest'

import { getCachedSystemName, resolvePageTitle } from '../route-title'

describe('resolvePageTitle', () => {
  test('keeps the server-injected title on the homepage', () => {
    expect(resolvePageTitle('/', 'Tokeness')).toBeNull()
    expect(resolvePageTitle('', 'Tokeness')).toBeNull()
  })

  test('formats non-home pages as "Page - System"', () => {
    expect(resolvePageTitle('/playground', 'Tokeness')).toBe(
      'Playground - Tokeness'
    )
    expect(resolvePageTitle('/keys', 'Tokeness')).toBe('API Keys - Tokeness')
    expect(resolvePageTitle('/privacy-policy', 'Tokeness')).toBe(
      'Privacy Policy - Tokeness'
    )
  })

  test('prefers the longest matching route rule', () => {
    expect(resolvePageTitle('/dashboard/overview', 'Tokeness')).toBe(
      'Overview - Tokeness'
    )
    expect(resolvePageTitle('/dashboard/models', 'Tokeness')).toBe(
      'Dashboard - Tokeness'
    )
    expect(resolvePageTitle('/usage-logs/task', 'Tokeness')).toBe(
      'Task Logs - Tokeness'
    )
  })

  test('does not cross-match path prefixes', () => {
    // /invoices must not hit the /invoice rule (and vice versa).
    expect(resolvePageTitle('/invoices', 'Tokeness')).toBe(
      'Invoice Review - Tokeness'
    )
    expect(resolvePageTitle('/invoice', 'Tokeness')).toBe(
      'Invoices - Tokeness'
    )
    expect(resolvePageTitle('/chat2link', 'Tokeness')).toBe(
      'Chat - Tokeness'
    )
  })

  test('matches nested paths by segment boundary', () => {
    expect(resolvePageTitle('/playground/abc', 'Tokeness')).toBe(
      'Playground - Tokeness'
    )
    expect(resolvePageTitle('/models/metadata', 'Tokeness')).toBe(
      'Models - Tokeness'
    )
  })

  test('falls back to a prettified segment for unknown paths', () => {
    expect(resolvePageTitle('/oauth-callback', 'Tokeness')).toBe(
      'Oauth Callback - Tokeness'
    )
  })

  test('falls back to the default system name when missing', () => {
    expect(resolvePageTitle('/playground')).toBe('Playground - Tokeness')
    expect(resolvePageTitle('/playground', '  ')).toBe(
      'Playground - Tokeness'
    )
  })
})

describe('getCachedSystemName', () => {
  test('reads system_name from the flat cached status payload', () => {
    const storage: Record<string, string> = {}
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage[key] ?? null,
      setItem: (key: string, value: string) => {
        storage[key] = value
      },
    })
    storage['status'] = JSON.stringify({ system_name: 'Tokeness', logo: 'x' })
    expect(getCachedSystemName()).toBe('Tokeness')

    storage['status'] = JSON.stringify({
      data: { system_name: 'Nested' },
    })
    expect(getCachedSystemName()).toBe('Nested')

    storage['status'] = JSON.stringify({ logo: 'x' })
    expect(getCachedSystemName()).toBeNull()
  })
})
