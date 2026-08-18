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
import { after, afterEach, describe, test } from 'node:test'

import { Window } from 'happy-dom'
import type React from 'react'

const domWindow = new Window({ url: 'https://tokeness.test/' })
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
  'localStorage',
  'HTMLElement',
  'HTMLTemplateElement',
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

Object.defineProperty(domWindow.HTMLElement.prototype, 'getAnimations', {
  configurable: true,
  value: () => [],
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { NotificationPopover } = await import('../notification-popover')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

async function renderWithI18n(element: React.ReactElement) {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(<I18nextProvider i18n={i18n}>{element}</I18nextProvider>)
  })

  return { container, root }
}

function TestNotificationPopover() {
  return (
    <NotificationPopover
      open
      onOpenChange={() => undefined}
      unreadCount={2}
      notice='Current service notice'
      announcements={[
        {
          id: 1,
          content: 'Timeline release update',
          extra: 'More release details',
          publishDate: '2026-08-17T00:00:00Z',
          type: 'success',
          unread: true,
        },
      ]}
      loading={false}
      onCloseToday={() => undefined}
    />
  )
}

describe('announcement dialog', () => {
  afterEach(() => {
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('separates the site notice from unread timeline announcements', async () => {
    const rendered = await renderWithI18n(<TestNotificationPopover />)

    const tabs = [
      ...document.querySelectorAll<HTMLButtonElement>('[role="tab"]'),
    ]
    const noticeTab = tabs.find((tab) => tab.textContent?.includes('Notice'))
    const timelineTab = tabs.find((tab) =>
      tab.textContent?.includes('Timeline')
    )

    assert.ok(noticeTab)
    assert.ok(timelineTab)
    assert.equal(timelineTab.getAttribute('aria-selected'), 'true')
    assert.equal(noticeTab.getAttribute('aria-selected'), 'false')
    assert.equal(
      document.body.textContent?.includes('Timeline release update'),
      true
    )
    assert.equal(
      document.body.textContent?.includes('More release details'),
      true
    )
    assert.ok(document.querySelector('[data-unread="true"]'))
    assert.ok(rendered.container.querySelector('button'))
    assert.equal(document.body.textContent?.includes('Close Today'), true)

    await act(async () => noticeTab.click())

    assert.equal(noticeTab.getAttribute('aria-selected'), 'true')
    assert.equal(timelineTab.getAttribute('aria-selected'), 'false')
    assert.equal(
      document.body.textContent?.includes('Current service notice'),
      true
    )
    assert.ok(
      rendered.container.querySelector('[aria-label="Notifications"]'),
      'the navigation bell remains the dialog trigger'
    )

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
  })
})
