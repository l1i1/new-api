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
  'HTMLTemplateElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { useSystemConfigStore } = await import('@/stores/system-config-store')
const { Footer } = await import('../footer')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'zh',
  fallbackLng: 'en',
  resources: {
    en: {
      translation: {
        'New API': 'New API',
        'footer.newapi.projectAttributionSuffix': 'Open source project',
      },
    },
    zh: {
      translation: {
        'New API': 'New API',
        'footer.newapi.projectAttributionSuffix': '开源项目',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('custom footer localization', () => {
  after(() => {
    domWindow.close()
  })

  test('switches localized HTML and retains project attribution', async () => {
    const previousState = useSystemConfigStore.getState()
    const footerHtml = [
      '<tnt l="zh"><strong>中文页脚</strong></tnt>',
      '<tnt l="en"><strong>English footer</strong></tnt>',
    ].join('')
    useSystemConfigStore.getState().setConfig({ footerHtml })
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    queryClient.setQueryData(['status'], {
      privacy_policy_enabled: false,
      user_agreement_enabled: false,
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <Footer />
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    assert.equal(container.textContent?.includes('中文页脚'), true)
    assert.equal(container.textContent?.includes('English footer'), false)
    assert.ok(
      container.querySelector(
        'a[href="https://github.com/QuantumNous/new-api"]'
      )
    )

    await act(async () => i18n.changeLanguage('en'))

    assert.equal(container.textContent?.includes('English footer'), true)
    assert.equal(container.textContent?.includes('中文页脚'), false)

    await act(async () => root.unmount())
    container.remove()
    queryClient.clear()
    useSystemConfigStore.setState(previousState, true)
  })
})
