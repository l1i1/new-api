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

import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { Window } from 'happy-dom'

import en from '@/i18n/locales/en.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import vi from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'

const domWindow = new Window({ url: 'https://tokeness.test/' })
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
  'customElements',
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
Object.defineProperty(globalThis, 'scrollTo', {
  configurable: true,
  value: () => undefined,
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { TokenessHome } = await import('../tokeness-home')
const { useHomeMetadata } = await import('../../hooks/use-home-metadata')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en, ja, ru, vi, zh, zhTW },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

async function renderHome() {
  const rootRoute = createRootRoute()
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: TokenessHome,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([homeRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <I18nextProvider i18n={i18n}>
        <RouterProvider router={router} />
      </I18nextProvider>
    )
  })

  return { container, root }
}

async function changeLanguage(language: string) {
  await act(async () => {
    await i18n.changeLanguage(language)
  })
}

function MetadataOwner() {
  useHomeMetadata('Configured New API')
  return null
}

describe('Tokeness legacy home', () => {
  after(() => {
    domWindow.close()
  })

  test('preserves the reviewed module order, routes, statistics, and provider order', async () => {
    await changeLanguage('en')
    const rendered = await renderHome()
    const home = rendered.container.querySelector(
      '[data-testid="tokeness-home"]'
    )

    assert.ok(home)
    assert.deepEqual(
      [...(home.firstElementChild?.children ?? [])].map(
        (element) => element.className
      ),
      [
        'tokeness-home__top-spacer',
        'tokeness-home__hero',
        'tokeness-home__capabilities',
        'tokeness-home__band',
        'tokeness-home__block tokeness-home__supplier',
        'tokeness-home__footer-grid',
        'tokeness-home__site-footer',
      ]
    )
    assert.ok(home.querySelector('a[href="/dashboard"]'))
    assert.equal(home.querySelectorAll('a[href="/pricing"]').length, 2)
    assert.deepEqual(
      [...home.querySelectorAll('.tokeness-home__stat')].map((element) =>
        element.textContent?.replaceAll(/\s+/g, ' ').trim()
      ),
      ['30+providers', '1gateway', '3layers']
    )
    assert.deepEqual(
      [...home.querySelectorAll('.tokeness-home__provider-tile[title]')].map(
        (element) => element.getAttribute('title')
      ),
      [
        'MoonshotAI',
        'OpenAI',
        'Grok',
        'Zhipu',
        'Volcengine',
        'Cohere',
        'Claude',
        'Gemini',
        'Suno',
        'Minimax',
        'Wenxin',
        'Spark',
        'Qingyan',
        'DeepSeek',
        'Qwen',
        'Midjourney',
        'Grok',
        'AzureAI',
        'Hunyuan',
        'Xinference',
      ]
    )

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
  })

  test('pins the three provider icons that changed in newer icon releases', async () => {
    await changeLanguage('en')
    const rendered = await renderHome()

    for (const provider of ['openai', 'gemini', 'qwen']) {
      assert.ok(
        rendered.container.querySelector(
          `[data-home-provider-icon="legacy-${provider}"]`
        )
      )
    }

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
  })

  test('uses only the Tokeness footer and retains linked New API attribution', async () => {
    await changeLanguage('zh')
    const rendered = await renderHome()
    const footer = rendered.container.querySelector(
      '.tokeness-home__site-footer'
    )

    assert.ok(footer)
    assert.equal(rendered.container.querySelectorAll('footer').length, 1)
    assert.match(
      footer.textContent ?? '',
      /© 2026 Tokeness\. All rights reserved\./
    )
    assert.equal(
      footer.querySelector<HTMLAnchorElement>(
        'a[href="https://github.com/QuantumNous/new-api"]'
      )?.textContent,
      '基于 New API 开发'
    )
    assert.equal(
      footer.querySelector<HTMLAnchorElement>(
        'a[href="https://lmspeed.net/provider/tokeness-cn"]'
      )?.rel,
      'noopener noreferrer'
    )

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
  })

  test('updates all legacy copy when the interface language changes', async () => {
    await changeLanguage('en')
    const rendered = await renderHome()

    assert.match(rendered.container.textContent ?? '', /One Entry, All Models/)
    await act(async () => {
      await i18n.changeLanguage('zh')
    })
    assert.match(rendered.container.textContent ?? '', /一个入口，所有模型/)
    assert.match(rendered.container.textContent ?? '', /基于 New API 开发/)

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
  })

  test('preserves the legacy Chinese normalization and compact fallback copy', async () => {
    await changeLanguage('zhTW')
    const rendered = await renderHome()

    assert.match(rendered.container.textContent ?? '', /一个入口，所有模型/)
    assert.doesNotMatch(
      rendered.container.textContent ?? '',
      /一個入口，所有模型/
    )

    await changeLanguage('ru')
    assert.match(
      rendered.container.textContent ?? '',
      /Create a Key and set allowed models and quota limits\./
    )
    assert.doesNotMatch(
      rendered.container.textContent ?? '',
      /Create a Key, set which models it can use and the quota limit\./
    )

    await act(async () => rendered.root.unmount())
    rendered.container.remove()
  })

  test('updates primary, Open Graph, and Twitter metadata and restores prior state', async () => {
    await changeLanguage('en')
    document.title = 'Previous title'
    const description = document.createElement('meta')
    description.name = 'description'
    description.content = 'Previous description'
    document.head.append(description)
    const openGraphTitle = document.createElement('meta')
    openGraphTitle.setAttribute('property', 'og:title')
    openGraphTitle.content = 'Previous Open Graph title'
    document.head.append(openGraphTitle)

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <MetadataOwner />
        </I18nextProvider>
      )
    })

    assert.equal(document.title, en.translation['home.meta.title'])
    assert.equal(openGraphTitle.content, en.translation['home.meta.title'])
    assert.equal(
      document.querySelector<HTMLMetaElement>('meta[name="twitter:title"]')
        ?.content,
      en.translation['home.meta.title']
    )
    assert.equal(
      document.querySelector<HTMLMetaElement>(
        'meta[name="twitter:description"]'
      )?.content,
      en.translation['home.meta.description']
    )

    await changeLanguage('zh')
    assert.equal(openGraphTitle.content, zh.translation['home.meta.title'])

    await act(async () => root.unmount())
    container.remove()

    assert.equal(document.title, 'Configured New API')
    assert.equal(description.content, 'Previous description')
    assert.equal(openGraphTitle.content, 'Previous Open Graph title')
    assert.equal(document.querySelector('meta[name="twitter:title"]'), null)
    assert.equal(
      document.querySelector('meta[name="twitter:description"]'),
      null
    )
    description.remove()
    openGraphTitle.remove()
  })
})
