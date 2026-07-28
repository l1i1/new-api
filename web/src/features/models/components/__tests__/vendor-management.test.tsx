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

import type { Vendor } from '../../types'

const domWindow = new Window({ url: 'https://tokeness.test/models/metadata' })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLButtonElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'MouseEvent',
  'PointerEvent',
  'KeyboardEvent',
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

const matchMedia = () => ({
  matches: false,
  addEventListener: () => undefined,
  removeEventListener: () => undefined,
})
Object.defineProperty(domWindow, 'matchMedia', {
  configurable: true,
  value: matchMedia,
})
Object.defineProperty(globalThis, 'matchMedia', {
  configurable: true,
  value: matchMedia,
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { api } = await import('@/lib/api')
const { ModelsPrimaryButtons } = await import('../models-primary-buttons')
const { ModelsProvider, useModels } = await import('../models-provider')
const { VendorManagementDialog } =
  await import('../dialogs/vendor-management-dialog')
const { VendorMutateDialog } = await import('../dialogs/vendor-mutate-dialog')

const originalApiGet = api.get
const originalApiPut = api.put
const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function findButton(name: string): HTMLButtonElement {
  const button = [...document.querySelectorAll('button')].find(
    (candidate) =>
      candidate.getAttribute('aria-label') === name ||
      candidate.textContent?.trim() === name
  )
  assert.ok(button, `Expected button "${name}"`)
  return button
}

function findControl(name: string): HTMLElement {
  const control = [
    ...document.querySelectorAll<HTMLElement>('button, [role]'),
  ].find(
    (candidate) =>
      candidate.getAttribute('aria-label') === name ||
      candidate.textContent?.trim() === name
  )
  assert.ok(control, `Expected control "${name}"`)
  return control
}

async function waitForText(text: string): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const check = () => {
      if (!document.body.textContent?.includes(text)) return false
      observer.disconnect()
      clearTimeout(timeout)
      resolve()
      return true
    }
    const observer = new MutationObserver(check)
    const timeout = setTimeout(() => {
      observer.disconnect()
      reject(new Error(`Timed out waiting for "${text}"`))
    }, 1000)

    observer.observe(document.body, {
      characterData: true,
      childList: true,
      subtree: true,
    })
    check()
  })
}

async function waitForEnabledButton(name: string): Promise<HTMLButtonElement> {
  await new Promise<void>((resolve, reject) => {
    const check = () => {
      const button = [...document.querySelectorAll('button')].find(
        (candidate) =>
          (candidate.getAttribute('aria-label') === name ||
            candidate.textContent?.trim() === name) &&
          !candidate.disabled
      )
      if (!button) return false
      observer.disconnect()
      clearTimeout(timeout)
      resolve()
      return true
    }
    const observer = new MutationObserver(check)
    const timeout = setTimeout(() => {
      observer.disconnect()
      reject(new Error(`Timed out waiting for enabled button "${name}"`))
    }, 1000)

    observer.observe(document.body, {
      attributes: true,
      childList: true,
      subtree: true,
    })
    check()
  })

  return findButton(name)
}

async function renderVendorManagement(options: {
  onCreateVendor: () => void
  onEditVendor: (vendor: Vendor) => void
  initialVendors?: Vendor[]
}) {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  if (options.initialVendors) {
    queryClient.setQueryData(['vendors', 'list', { p: 1, page_size: 100 }], {
      success: true,
      data: {
        items: options.initialVendors,
        total: options.initialVendors.length,
        page: 1,
        page_size: 100,
      },
    })
  }

  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <VendorManagementDialog
            open
            onOpenChange={() => undefined}
            onCreateVendor={options.onCreateVendor}
            onEditVendor={options.onEditVendor}
          />
        </I18nextProvider>
      </QueryClientProvider>
    )
  })
  return { queryClient, root }
}

async function renderVendorMutate(currentVendor: Vendor) {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <VendorMutateDialog
            open
            onOpenChange={() => undefined}
            currentVendor={currentVendor}
          />
        </I18nextProvider>
      </QueryClientProvider>
    )
  })

  return { queryClient, root }
}

describe('vendor management dialog', () => {
  afterEach(() => {
    api.get = originalApiGet
    api.put = originalApiPut
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('opens vendor management from the models actions menu', async () => {
    function DialogStateProbe() {
      const { open } = useModels()
      return <output>{open || 'closed'}</output>
    }

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <ModelsProvider>
            <ModelsPrimaryButtons />
            <DialogStateProbe />
          </ModelsProvider>
        </I18nextProvider>
      )
    })

    await act(async () => {
      findButton('More').focus()
      findButton('More').dispatchEvent(
        new KeyboardEvent('keydown', { bubbles: true, key: 'ArrowDown' })
      )
    })

    await act(async () => waitForText('Manage Vendors'))
    const manageButton = findControl('Manage Vendors')
    await act(async () => manageButton.click())

    assert.equal(
      container.querySelector('output')?.textContent,
      'manage-vendors'
    )
    await act(async () => root.unmount())
  })

  test('loads existing vendors and sends the selected vendor to edit', async () => {
    const vendor: Vendor = {
      id: 7,
      name: 'Alibaba',
      display_name: '<tnt l="zh">阿里巴巴</tnt><tnt l="en">Alibaba</tnt>',
      description: 'Cloud vendor',
      status: 1,
      created_time: 1,
      updated_time: 1,
    }
    api.get = (async (url: string) => {
      assert.equal(url, '/api/vendors/')
      return {
        data: {
          success: true,
          data: { items: [vendor], total: 1, page: 1, page_size: 100 },
        },
      }
    }) as typeof api.get
    const edited: Vendor[] = []
    const rendered = await renderVendorManagement({
      onCreateVendor: () => undefined,
      onEditVendor: (selected) => edited.push(selected),
      initialVendors: [vendor],
    })

    await waitForText('Cloud vendor')
    await act(async () => findButton('Edit Vendor: Alibaba').click())
    assert.deepEqual(edited, [vendor])

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })

  test('offers a reachable create action when no vendors exist', async () => {
    api.get = (async () => ({
      data: {
        success: true,
        data: { items: [], total: 0, page: 1, page_size: 100 },
      },
    })) as typeof api.get
    let createRequests = 0
    const rendered = await renderVendorManagement({
      onCreateVendor: () => {
        createRequests += 1
      },
      onEditVendor: () => undefined,
    })

    await act(async () => findButton('Create Vendor').click())
    assert.equal(createRequests, 1)

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })

  test('loads vendors beyond the first backend page', async () => {
    const firstVendor: Vendor = {
      id: 1,
      name: 'First Vendor',
      status: 1,
      created_time: 1,
      updated_time: 1,
    }
    const lastVendor: Vendor = {
      id: 101,
      name: 'Last Vendor',
      status: 1,
      created_time: 1,
      updated_time: 1,
    }
    const requestedPages: number[] = []
    api.get = (async (
      _url: string,
      config?: { params?: { p?: number; page_size?: number } }
    ) => {
      const page = config?.params?.p ?? 1
      assert.equal(config?.params?.page_size, 100)
      requestedPages.push(page)
      return {
        data: {
          success: true,
          data: {
            items: page === 1 ? [firstVendor] : [lastVendor],
            total: 101,
            page,
            page_size: 100,
          },
        },
      }
    }) as typeof api.get

    const rendered = await renderVendorManagement({
      onCreateVendor: () => undefined,
      onEditVendor: () => undefined,
    })

    await act(async () => waitForText('First Vendor'))
    assert.ok(document.body.textContent?.includes('Page 1 of 2'))

    const nextButton = await waitForEnabledButton('Next')
    await act(async () => {
      nextButton.click()
      await new Promise((resolve) => setTimeout(resolve, 50))
    })

    assert.deepEqual(requestedPages, [1, 2])
    const secondPage = rendered.queryClient.getQueryData<{
      data?: { items?: Vendor[] }
    }>(['vendors', 'list', { p: 2, page_size: 100 }])
    assert.deepEqual(secondPage?.data?.items, [lastVendor])

    await act(async () => {
      await rendered.queryClient.cancelQueries({ queryKey: ['vendors'] })
    })
    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })

  test('preserves a disabled vendor status when editing metadata', async () => {
    const vendor: Vendor = {
      id: 8,
      name: 'Disabled Vendor',
      display_name: '<tnt l="en">Disabled Vendor</tnt>',
      status: 0,
      created_time: 1,
      updated_time: 1,
    }
    const submitted: Array<Record<string, unknown>> = []
    api.put = (async (_url: string, data: Record<string, unknown>) => {
      submitted.push(data)
      return { data: { success: true } }
    }) as typeof api.put

    const rendered = await renderVendorMutate(vendor)
    await waitForText('Disabled Vendor')
    await act(async () => {
      findButton('Update').click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    assert.equal(submitted.length, 1)
    assert.equal(submitted[0]?.status, 0)

    await act(async () => rendered.root.unmount())
    rendered.queryClient.clear()
  })
})
