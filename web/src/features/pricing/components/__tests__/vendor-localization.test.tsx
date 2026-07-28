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

import type { AccessorFnColumnDef } from '@tanstack/react-table'
import { Window } from 'happy-dom'

import type { PricingModel, PricingVendor } from '../../types'

const domWindow = new Window({ url: 'https://tokeness.test/pricing' })
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
  'customElements',
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
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { usePricingColumns } = await import('../pricing-columns')
const { PricingSidebar } = await import('../pricing-sidebar')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: {
    en: { translation: {} },
    zh: { translation: {} },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const vendorName = '阿里巴巴'
const vendorDisplayName =
  '<tnt l="zh">阿里云</tnt><tnt l="en">Alibaba Cloud</tnt>'
const vendors: PricingVendor[] = [
  { id: 1, name: vendorName, display_name: vendorDisplayName },
]
const models: PricingModel[] = [
  {
    id: 1,
    model_name: 'qwen-test',
    vendor_id: 1,
    vendor_name: vendorName,
    vendor_display_name: vendorDisplayName,
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 1,
    enable_groups: ['default'],
    localized_tags: 'Free',
  },
]

describe('pricing vendor localization', () => {
  after(() => {
    domWindow.close()
  })

  test('changes the visible vendor label while preserving the raw filter value', async () => {
    const selectedVendors: string[] = []
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PricingSidebar
            quotaTypeFilter='all'
            endpointTypeFilter='all'
            vendorFilter='all'
            groupFilter='all'
            tagFilter='all'
            onQuotaTypeChange={() => undefined}
            onEndpointTypeChange={() => undefined}
            onVendorChange={(value) => selectedVendors.push(value)}
            onGroupChange={() => undefined}
            onTagChange={() => undefined}
            vendors={vendors}
            groups={[]}
            tags={[]}
            models={models}
            hasActiveFilters={false}
            onClearFilters={() => undefined}
          />
        </I18nextProvider>
      )
    })

    const findVendorButton = (label: string) =>
      [...container.querySelectorAll('button')].find((button) =>
        button.textContent?.includes(label)
      ) as HTMLButtonElement | undefined

    const englishButton = findVendorButton('Alibaba Cloud')
    assert.ok(englishButton)
    assert.equal(container.textContent?.includes('<tnt'), false)

    await act(async () => englishButton.click())
    assert.deepEqual(selectedVendors, [vendorName])

    await act(async () => i18n.changeLanguage('zh'))

    assert.ok(findVendorButton('阿里云'))
    assert.equal(findVendorButton('Alibaba Cloud'), undefined)

    await act(async () => root.unmount())
    container.remove()
  })

  test('falls back to the canonical vendor name when display_name is empty', async () => {
    await i18n.changeLanguage('en')
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PricingSidebar
            quotaTypeFilter='all'
            endpointTypeFilter='all'
            vendorFilter='all'
            groupFilter='all'
            tagFilter='all'
            onQuotaTypeChange={() => undefined}
            onEndpointTypeChange={() => undefined}
            onVendorChange={() => undefined}
            onGroupChange={() => undefined}
            onTagChange={() => undefined}
            vendors={[{ id: 2, name: 'OpenAI' }]}
            groups={[]}
            tags={[]}
            models={[
              {
                id: 2,
                model_name: 'gpt-test',
                vendor_id: 2,
                vendor_name: 'OpenAI',
                quota_type: 0,
                model_ratio: 1,
                completion_ratio: 1,
                enable_groups: ['default'],
              },
            ]}
            hasActiveFilters={false}
            onClearFilters={() => undefined}
          />
        </I18nextProvider>
      )
    })

    assert.ok(container.textContent?.includes('OpenAI'))

    await act(async () => root.unmount())
    container.remove()
  })

  test('sorts the vendor column by the currently visible localized name', async () => {
    function VendorSortProbe() {
      const columns = usePricingColumns()
      const vendorColumn = columns.find((column) => column.id === 'vendor') as
        | AccessorFnColumnDef<PricingModel>
        | undefined
      const accessor = vendorColumn?.accessorFn
      assert.ok(accessor)

      const alibabaModel = {
        ...models[0],
        vendor_localized_name: i18n.language === 'zh' ? '阿里云' : 'Alibaba',
      }
      const zhipuModel = {
        ...models[0],
        id: 2,
        vendor_name: '智谱',
        vendor_localized_name: i18n.language === 'zh' ? '智谱' : 'Zhipu',
      }

      return (
        <output>
          {String(accessor(alibabaModel, 0))}|{String(accessor(zhipuModel, 1))}
        </output>
      )
    }

    await i18n.changeLanguage('en')
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <VendorSortProbe />
        </I18nextProvider>
      )
    })
    assert.equal(container.textContent, 'Alibaba|Zhipu')

    await act(async () => i18n.changeLanguage('zh'))
    assert.equal(container.textContent, '阿里云|智谱')

    await act(async () => root.unmount())
    container.remove()
  })

  test('renders localized model tags without exposing tnt markup', async () => {
    await i18n.changeLanguage('en')
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <PricingSidebar
            quotaTypeFilter='all'
            endpointTypeFilter='all'
            vendorFilter='all'
            groupFilter='all'
            tagFilter='all'
            onQuotaTypeChange={() => undefined}
            onEndpointTypeChange={() => undefined}
            onVendorChange={() => undefined}
            onGroupChange={() => undefined}
            onTagChange={() => undefined}
            vendors={vendors}
            groups={[]}
            tags={['free']}
            models={models}
            hasActiveFilters={false}
            onClearFilters={() => undefined}
          />
        </I18nextProvider>
      )
    })

    assert.ok(container.textContent?.includes('free'))
    assert.equal(container.textContent?.includes('<tnt'), false)
    assert.equal(container.textContent?.includes('免费'), false)

    await act(async () => root.unmount())
    container.remove()
  })
})
