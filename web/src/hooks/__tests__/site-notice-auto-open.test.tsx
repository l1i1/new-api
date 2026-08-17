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

const domWindow = new Window({ url: 'https://tokeness.test/pricing' })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { defaultScheduler, notifyManager, QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { useNotificationStore } = await import('@/stores/notification-store')
const {
  getNotificationAutoOpenOptions,
  getNotificationRefreshInterval,
  useNotifications,
} = await import('../use-notifications')

const originalApiGet = api.get
const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true
notifyManager.setScheduler((callback) => callback())

type AutoOpenOptions = {
  autoOpenNotice?: boolean
  autoOpenPopover?: boolean
}

function NoticeProbe(props: AutoOpenOptions) {
  const notifications = useNotifications({
    autoOpenNotice: props.autoOpenNotice,
    autoOpenPopover: props.autoOpenPopover,
  })

  return (
    <div>
      <output aria-label='site notice state'>
        {notifications.siteNoticeOpen
          ? `open:${notifications.notice}`
          : 'closed'}
      </output>
      <output aria-label='notification popover state'>
        {notifications.popoverOpen ? `open:${notifications.notice}` : 'closed'}
      </output>
      <button
        type='button'
        aria-label='close site notice'
        onClick={() => notifications.setSiteNoticeOpen(false)}
      >
        Close site notice
      </button>
      <button
        type='button'
        aria-label='close notification popover'
        onClick={() => notifications.closePopover()}
      >
        Close notification popover
      </button>
    </div>
  )
}

function createTestApp(options: AutoOpenOptions) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  queryClient.setQueryData(['status'], {
    announcements_enabled: false,
    announcements: [],
  })
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  return {
    container,
    queryClient,
    root,
    render: () =>
      act(async () => {
        root.render(
          <QueryClientProvider client={queryClient}>
            <NoticeProbe {...options} />
          </QueryClientProvider>
        )
        await queryClient.refetchQueries({ queryKey: ['notice'] })
      }),
  }
}

async function waitForState(
  container: HTMLElement,
  label: string,
  expected: string
): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const check = () => {
      const output = container.querySelector(`output[aria-label="${label}"]`)
      if (output?.textContent !== expected) return false
      observer.disconnect()
      clearTimeout(timeout)
      resolve()
      return true
    }
    const observer = new MutationObserver(check)
    const timeout = setTimeout(() => {
      observer.disconnect()
      reject(new Error(`Timed out waiting for ${label} state "${expected}"`))
    }, 1000)

    observer.observe(container, {
      characterData: true,
      childList: true,
      subtree: true,
    })
    check()
  })
}

async function destroyTestApp(app: ReturnType<typeof createTestApp>) {
  await act(async () => app.root.unmount())
  app.container.remove()
  app.queryClient.clear()
}

afterEach(() => {
  api.get = originalApiGet
  window.localStorage.clear()
  useNotificationStore.setState({
    lastReadNotice: '',
    lastAutoOpenedPricingNotice: '',
    readAnnouncementKeys: [],
    closedUntilDate: null,
    pendingAutoOpenKey: null,
  })
  document.body.replaceChildren()
})

after(() => {
  notifyManager.setScheduler(defaultScheduler)
  domWindow.close()
})

describe('site notice automatic display', () => {
  test('opens once for the current notice and opens again after it changes', async () => {
    let notice = 'Initial site notice'
    api.get = (async (url: string) => {
      if (url === '/api/notice') {
        return { data: { success: true, data: notice } }
      }
      throw new Error(`Unexpected API request: ${url}`)
    }) as typeof api.get

    const app = createTestApp({ autoOpenNotice: true })
    await app.render()
    await waitForState(
      app.container,
      'site notice state',
      'open:Initial site notice'
    )

    assert.equal(
      useNotificationStore.getState().lastAutoOpenedPricingNotice,
      'Initial site notice'
    )

    await act(async () => {
      app.container
        .querySelector<HTMLButtonElement>('[aria-label="close site notice"]')
        ?.click()
    })
    await waitForState(app.container, 'site notice state', 'closed')

    notice = 'Updated site notice'
    await act(async () => {
      await app.queryClient.invalidateQueries({ queryKey: ['notice'] })
    })
    await waitForState(
      app.container,
      'site notice state',
      'open:Updated site notice'
    )

    await act(async () => {
      app.container
        .querySelector<HTMLButtonElement>('[aria-label="close site notice"]')
        ?.click()
    })
    await act(async () => {
      await app.queryClient.invalidateQueries({ queryKey: ['notice'] })
    })
    await waitForState(app.container, 'site notice state', 'closed')

    await destroyTestApp(app)
  })

  test('reopens after the pricing header remounts during page loading', async () => {
    api.get = (async (url: string) => {
      if (url === '/api/notice') {
        return { data: { success: true, data: 'Pricing notice' } }
      }
      throw new Error(`Unexpected API request: ${url}`)
    }) as typeof api.get

    const firstHeader = createTestApp({ autoOpenNotice: true })
    await firstHeader.render()
    await waitForState(
      firstHeader.container,
      'site notice state',
      'open:Pricing notice'
    )

    await act(async () => firstHeader.root.unmount())
    firstHeader.container.remove()
    firstHeader.queryClient.clear()

    const loadedHeader = createTestApp({ autoOpenNotice: true })
    await loadedHeader.render()
    await waitForState(
      loadedHeader.container,
      'site notice state',
      'open:Pricing notice'
    )

    await destroyTestApp(loadedHeader)
  })

  test('does not open when automatic display is disabled', async () => {
    api.get = (async (url: string) => {
      if (url === '/api/notice') {
        return { data: { success: true, data: 'Home page notice' } }
      }
      throw new Error(`Unexpected API request: ${url}`)
    }) as typeof api.get

    const app = createTestApp({})
    await app.render()
    await waitForState(app.container, 'site notice state', 'closed')
    await waitForState(app.container, 'notification popover state', 'closed')

    await destroyTestApp(app)
  })
})

describe('notification popover automatic display', () => {
  test('refreshes announcements at a bounded interval when polling is enabled', () => {
    assert.equal(getNotificationRefreshInterval(true), 5 * 60 * 1000)
    assert.equal(getNotificationRefreshInterval(false), false)
  })

  test('selects one automatic surface based on the current route', () => {
    assert.deepEqual(getNotificationAutoOpenOptions('/'), {
      autoOpenNotice: false,
      autoOpenPopover: false,
    })
    assert.deepEqual(getNotificationAutoOpenOptions('/pricing/'), {
      autoOpenNotice: true,
      autoOpenPopover: false,
    })
    assert.deepEqual(getNotificationAutoOpenOptions('/dashboard/'), {
      autoOpenNotice: false,
      autoOpenPopover: true,
    })
    assert.deepEqual(getNotificationAutoOpenOptions('/pricing/model-id'), {
      autoOpenNotice: false,
      autoOpenPopover: true,
    })
  })

  test('opens for an unread notice and stays closed for the same revision', async () => {
    api.get = (async (url: string) => {
      if (url === '/api/notice') {
        return { data: { success: true, data: 'Dashboard notice' } }
      }
      throw new Error(`Unexpected API request: ${url}`)
    }) as typeof api.get

    const app = createTestApp({ autoOpenPopover: true })
    await app.render()
    await waitForState(
      app.container,
      'notification popover state',
      'open:Dashboard notice'
    )
    assert.equal(
      useNotificationStore.getState().lastReadNotice,
      'Dashboard notice'
    )

    await act(async () => {
      app.container
        .querySelector<HTMLButtonElement>(
          '[aria-label="close notification popover"]'
        )
        ?.click()
    })
    await waitForState(app.container, 'notification popover state', 'closed')

    await act(async () => {
      await app.queryClient.invalidateQueries({ queryKey: ['notice'] })
    })
    await waitForState(app.container, 'notification popover state', 'closed')

    await destroyTestApp(app)
  })

  test('opens again when the notice revision changes', async () => {
    let notice = 'Initial dashboard notice'
    api.get = (async (url: string) => {
      if (url === '/api/notice') {
        return { data: { success: true, data: notice } }
      }
      throw new Error(`Unexpected API request: ${url}`)
    }) as typeof api.get

    const app = createTestApp({ autoOpenPopover: true })
    await app.render()
    await waitForState(
      app.container,
      'notification popover state',
      'open:Initial dashboard notice'
    )

    await act(async () => {
      app.container
        .querySelector<HTMLButtonElement>(
          '[aria-label="close notification popover"]'
        )
        ?.click()
    })
    notice = 'Updated dashboard notice'
    await act(async () => {
      await app.queryClient.invalidateQueries({ queryKey: ['notice'] })
    })
    await waitForState(
      app.container,
      'notification popover state',
      'open:Updated dashboard notice'
    )

    await destroyTestApp(app)
  })

  test('opens for timeline announcements and for edits to an existing item', async () => {
    api.get = (async (url: string) => {
      if (url === '/api/notice') {
        return { data: { success: true, data: '' } }
      }
      throw new Error(`Unexpected API request: ${url}`)
    }) as typeof api.get

    const app = createTestApp({ autoOpenPopover: true })
    app.queryClient.setQueryData(['status'], {
      announcements_enabled: true,
      announcements: [
        {
          id: 7,
          content: 'Initial timeline update',
          publishDate: '2026-08-17T00:00:00Z',
          type: 'ongoing',
        },
      ],
    })
    await app.render()
    await waitForState(app.container, 'notification popover state', 'open:')
    assert.equal(useNotificationStore.getState().readAnnouncementKeys.length, 1)

    await act(async () => {
      app.container
        .querySelector<HTMLButtonElement>(
          '[aria-label="close notification popover"]'
        )
        ?.click()
    })
    await waitForState(app.container, 'notification popover state', 'closed')

    await act(async () => {
      app.queryClient.setQueryData(['status'], {
        announcements_enabled: true,
        announcements: [
          {
            id: 7,
            content: 'Edited timeline update',
            publishDate: '2026-08-17T00:00:00Z',
            type: 'ongoing',
          },
        ],
      })
    })
    await waitForState(app.container, 'notification popover state', 'open:')
    assert.equal(useNotificationStore.getState().readAnnouncementKeys.length, 2)

    await destroyTestApp(app)
  })
})
