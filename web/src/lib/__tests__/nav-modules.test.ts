/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

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
  HEADER_NAV_DEFAULT,
  moveHeaderNavItem,
} from '../../features/system-settings/maintenance/config'
import { buildTopNavLinks } from '../../hooks/use-top-nav-links'
import { isSafeHeaderNavUrl, parseHeaderNavModules } from '../nav-modules'

describe('header navigation configuration', () => {
  test('moves navigation entries without mutating the source order', () => {
    const items = HEADER_NAV_DEFAULT.items

    assert.deepEqual(
      moveHeaderNavItem(items, 'rankings', -1).map((item) => item.id),
      ['console', 'rankings', 'pricing', 'docs', 'about']
    )
    assert.deepEqual(
      items.map((item) => item.id),
      ['console', 'pricing', 'rankings', 'docs', 'about']
    )
  })

  test('keeps Home fixed and migrates legacy boolean settings', () => {
    const modules = parseHeaderNavModules(
      JSON.stringify({ home: false, pricing: false, docs: true })
    )

    assert.equal(modules.home, true)
    assert.equal(modules.pricing.enabled, false)
    assert.deepEqual(
      modules.items.map((item) => item.id),
      ['console', 'pricing', 'rankings', 'docs', 'about']
    )
    assert.equal(
      modules.items.find((item) => item.id === 'pricing')?.visible,
      false
    )
  })

  test('preserves configured order, visibility, and custom target behavior', () => {
    const modules = parseHeaderNavModules({
      home: false,
      items: [
        {
          id: 'about',
          title: 'About',
          url: '/about',
          newTab: false,
          visible: true,
        },
        {
          id: 'custom-docs',
          title: 'Team docs',
          url: 'https://docs.example.com/team',
          newTab: true,
          visible: true,
        },
        {
          id: 'pricing',
          title: 'Model Square',
          url: '/pricing',
          newTab: false,
          visible: false,
        },
      ],
    })

    assert.equal(modules.home, true)
    assert.deepEqual(
      modules.items.slice(0, 3).map((item) => item.id),
      ['about', 'custom-docs', 'pricing']
    )
    assert.equal(modules.pricing.enabled, false)
    assert.equal(modules.items[1].newTab, true)
    assert.equal(modules.items[1].visible, true)
  })

  test('accepts only safe internal or HTTP(S) URLs', () => {
    assert.equal(isSafeHeaderNavUrl('/docs/getting-started'), true)
    assert.equal(isSafeHeaderNavUrl('https://docs.example.com'), true)
    assert.equal(isSafeHeaderNavUrl('http://localhost:3000/docs'), true)

    assert.equal(isSafeHeaderNavUrl('//evil.example.com'), false)
    assert.equal(isSafeHeaderNavUrl('javascript:alert(1)'), false)
    assert.equal(isSafeHeaderNavUrl('data:text/html,hello'), false)
    assert.equal(isSafeHeaderNavUrl('/docs\\evil'), false)
    assert.equal(isSafeHeaderNavUrl('/docs\u0000evil'), false)
  })

  test('drops malformed custom entries without affecting built-ins', () => {
    const modules = parseHeaderNavModules({
      items: [
        {
          id: 'unsafe',
          title: 'Unsafe',
          url: 'javascript:alert(1)',
          newTab: true,
          visible: true,
        },
      ],
    })

    assert.equal(
      modules.items.some((item) => item.id === 'unsafe'),
      false
    )
    assert.deepEqual(
      modules.items.map((item) => item.id),
      ['console', 'pricing', 'rankings', 'docs', 'about']
    )
  })

  test('renders Home first and follows the configured visible order', () => {
    const modules = parseHeaderNavModules({
      items: [
        {
          id: 'custom-help',
          title: 'Help',
          url: 'https://help.example.com',
          newTab: true,
          visible: true,
        },
        {
          id: 'about',
          title: 'About',
          url: '/about',
          newTab: false,
          visible: true,
        },
        {
          id: 'console',
          title: 'Console',
          url: '/dashboard',
          newTab: false,
          visible: false,
        },
      ],
    })

    const links = buildTopNavLinks(modules, undefined, false, (key) => key)

    assert.deepEqual(
      links.map((link) => link.title),
      ['Home', 'Help', 'About', 'Model Square', 'Rankings', 'Docs']
    )
    assert.equal(links[1].external, true)
    assert.equal(links[1].newTab, true)
  })

  test('keeps external documentation links opening in a new tab', () => {
    const modules = parseHeaderNavModules('')
    const links = buildTopNavLinks(
      modules,
      'https://docs.example.com',
      false,
      (key) => key
    )
    const docs = links.find((link) => link.title === 'Docs')

    assert.equal(docs?.external, true)
    assert.equal(docs?.newTab, true)
  })
})
