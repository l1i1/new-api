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
import { after, describe, test } from 'node:test'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Window } from 'happy-dom'

import type { AnnouncementItem } from '@/features/dashboard/types'

const domWindow = new Window()
domWindow.document.write(
  '<!doctype html><html><head></head><body></body></html>'
)
Object.defineProperty(domWindow.document, 'compatMode', {
  configurable: true,
  value: 'CSS1Compat',
})
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

Object.defineProperty(domWindow.Element.prototype, 'getAnimations', {
  configurable: true,
  value: () => [],
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { AnnouncementsPanel } = await import('../announcements-panel')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'zh',
  fallbackLng: 'en',
  resources: {
    en: {
      translation: {
        Announcements: 'Announcements',
        'Latest platform updates and notices':
          'Latest platform updates and notices',
        'No announcements at this time': 'No announcements at this time',
        'Click for details': 'Click for details',
      },
    },
    zh: {
      translation: {
        Announcements: '系统公告',
        'Latest platform updates and notices': '最新平台动态与通知',
        'No announcements at this time': '暂无公告',
        'Click for details': '点击查看详情',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('announcement preview localization', () => {
  after(() => {
    domWindow.close()
  })

  test('updates the preview language without mutating the raw announcement', async () => {
    const content = [
      '<tnt l="zh">**中文公告**：维护完成</tnt>',
      '<tnt l="en">**English notice**: Maintenance complete</tnt>',
    ].join('')
    const announcement: AnnouncementItem = {
      id: 42,
      content,
      type: 'success',
    }
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    queryClient.setQueryData(['status'], {
      announcements_enabled: true,
      announcements: [announcement],
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <AnnouncementsPanel />
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    assert.equal(container.textContent?.includes('中文公告：维护完成'), true)
    assert.equal(container.textContent?.includes('English notice'), false)

    await act(async () => i18n.changeLanguage('en'))

    assert.equal(
      container.textContent?.includes('English notice: Maintenance complete'),
      true
    )
    assert.equal(container.textContent?.includes('中文公告'), false)
    assert.equal(announcement.content, content)

    await act(async () => root.unmount())
    container.remove()
    queryClient.clear()
  })
})
