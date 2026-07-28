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

const { act, useEffect } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { api } = await import('@/lib/api')
const { usePricingData } = await import('../use-pricing-data')

const originalApiGet = api.get
const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} }, zh: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

async function waitForText(text: string): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const check = () => {
      if (document.body.textContent !== text) return false
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

describe('usePricingData vendor localization', () => {
  afterEach(() => {
    api.get = originalApiGet
    document.body.replaceChildren()
  })

  after(() => {
    domWindow.close()
  })

  test('recomputes the searchable vendor name when the language changes', async () => {
    api.get = (async (url: string) => {
      throw new Error(`Unexpected API request: ${url}`)
    }) as typeof api.get
    await i18n.changeLanguage('en')

    const observedNames: string[] = []
    function Probe() {
      const { models } = usePricingData()
      const name = models[0]?.vendor_localized_name
      useEffect(() => {
        if (name) observedNames.push(name)
      }, [name])
      return <span>{name}</span>
    }

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    queryClient.setQueryData(['status'], {
      price: 1,
      usd_exchange_rate: 1,
    })
    queryClient.setQueryData(['pricing'], {
      success: true,
      data: [
        {
          id: 1,
          model_name: 'qwen-test',
          vendor_id: 7,
          quota_type: 0,
          model_ratio: 1,
          completion_ratio: 1,
          enable_groups: ['default'],
        },
      ],
      vendors: [
        {
          id: 7,
          name: '阿里巴巴',
          display_name: '<tnt l="zh">阿里巴巴</tnt><tnt l="en">Alibaba</tnt>',
        },
      ],
      group_ratio: {},
      usable_group: {},
      supported_endpoint: {},
      auto_groups: [],
    })

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <Probe />
          </I18nextProvider>
        </QueryClientProvider>
      )
    })
    await waitForText('Alibaba')
    assert.equal(container.textContent, 'Alibaba')

    await act(async () => {
      await i18n.changeLanguage('zh')
    })
    await waitForText('阿里巴巴')
    assert.equal(container.textContent, '阿里巴巴')
    assert.deepEqual(observedNames, ['Alibaba', '阿里巴巴'])

    await act(async () => root.unmount())
    queryClient.clear()
  })
})
