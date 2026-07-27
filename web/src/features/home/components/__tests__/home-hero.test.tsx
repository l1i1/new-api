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
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { Window } from 'happy-dom'

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

Object.defineProperty(domWindow, 'matchMedia', {
  configurable: true,
  value: () => ({
    matches: true,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }),
})
Object.defineProperty(globalThis, 'scrollTo', {
  configurable: true,
  value: () => undefined,
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { Hero } = await import('../sections/hero')
const { useHomeMetadata } = await import('../../hooks/use-home-metadata')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        Docs: 'Docs',
        'Get Started': 'Get Started',
        'Go to Dashboard': 'Go to Dashboard',
        'View Pricing': 'View Pricing',
        'home.hero.copy': 'One key for every model.',
        'home.hero.kicker': 'AI ROUTING & CONTROL',
        'home.hero.protocols': 'One key across compatible APIs.',
        'home.hero.title': 'One entry. Every model.',
        'home.meta.description': 'English description',
        'home.meta.keywords': 'English keywords',
        'home.meta.title': 'Tokeness English title',
      },
    },
    zh: {
      translation: {
        Docs: '文档',
        'Get Started': '开始使用',
        'Go to Dashboard': '进入控制台',
        'View Pricing': '查看价格',
        'home.hero.copy': '一个 Key 调所有模型。',
        'home.hero.kicker': 'AI 路由与控制',
        'home.hero.protocols': '一个 Key 通用兼容接口。',
        'home.hero.title': '一个入口，所有模型',
        'home.meta.description': '中文描述',
        'home.meta.keywords': '中文关键词',
        'home.meta.title': 'Tokeness 中文标题',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('Tokeness home hero', () => {
  after(() => {
    domWindow.close()
  })

  test('updates localized metadata and restores the previous document state', async () => {
    document.title = 'Previous title'
    const existingTitle = document.createElement('meta')
    existingTitle.name = 'title'
    existingTitle.content = 'New API'
    document.head.append(existingTitle)
    const existingDescription = document.createElement('meta')
    existingDescription.name = 'description'
    existingDescription.content = 'Previous description'
    document.head.append(existingDescription)

    const queryClient = new QueryClient()
    queryClient.setQueryData(['status'], { docs_link: '/docs' })
    function HomeMetadataOwner() {
      useHomeMetadata('Configured New API')
      return <Hero />
    }
    const rootRoute = createRootRoute()
    const homeRoute = createRoute({
      getParentRoute: () => rootRoute,
      path: '/',
      component: HomeMetadataOwner,
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
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <RouterProvider router={router} />
          </I18nextProvider>
        </QueryClientProvider>
      )
    })

    assert.equal(document.title, 'Tokeness English title')
    assert.equal(existingDescription.content, 'English description')
    assert.equal(
      document.querySelector<HTMLMetaElement>('meta[name="keywords"]')?.content,
      'English keywords'
    )
    assert.equal(container.textContent?.includes('Tokeness'), true)
    assert.ok(container.querySelector('a[href="/sign-up"]'))
    assert.ok(container.querySelector('a[href="/pricing"]'))

    await act(async () => {
      await i18n.changeLanguage('zh')
    })

    assert.equal(document.title, 'Tokeness 中文标题')
    assert.equal(existingDescription.content, '中文描述')

    await act(async () => root.unmount())
    container.remove()
    queryClient.clear()

    assert.equal(document.title, 'Configured New API')
    assert.equal(existingTitle.content, 'Configured New API')
    assert.equal(existingDescription.content, 'Previous description')
    assert.equal(document.querySelector('meta[name="keywords"]'), null)
    existingTitle.remove()
    existingDescription.remove()
  })

  test('owns metadata when a custom home page replaces the default hero', async () => {
    await i18n.changeLanguage('en')
    document.title = 'New API'

    function CustomHomeMetadataOwner() {
      useHomeMetadata('Configured New API')
      return <main>Custom home</main>
    }

    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <CustomHomeMetadataOwner />
        </I18nextProvider>
      )
    })

    assert.equal(document.title, 'Tokeness English title')
    assert.equal(container.textContent, 'Custom home')

    await act(async () => root.unmount())
    container.remove()

    assert.equal(document.title, 'Configured New API')
    assert.equal(document.querySelector('meta[name="title"]'), null)
    assert.equal(document.querySelector('meta[name="description"]'), null)
    assert.equal(document.querySelector('meta[name="keywords"]'), null)
  })
})
