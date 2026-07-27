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
const { api } = await import('@/lib/api')
const { isLikelyHtml } = await import('@/lib/content-format')
const { RichContent } = await import('../rich-content')
const { useHomePageContent } =
  await import('@/features/home/hooks/use-home-page-content')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'zh',
  fallbackLng: 'en',
  resources: {
    en: { translation: {} },
    zh: { translation: {} },
  },
})

const originalApiGet = api.get
const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

type RenderedComponent = {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}

async function renderWithI18n(element: React.ReactElement) {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(<I18nextProvider i18n={i18n}>{element}</I18nextProvider>)
  })

  return { container, root }
}

async function unmountComponent(rendered: RenderedComponent) {
  await act(async () => rendered.root.unmount())
  rendered.container.remove()
}

function HomeContentProbe() {
  const result = useHomePageContent()
  let kind = 'loading'

  if (result.isLoaded) {
    if (result.isUrl) {
      kind = 'url'
    } else if (isLikelyHtml(result.content)) {
      kind = 'html'
    } else {
      kind = 'markdown'
    }
  }

  return <output data-kind={kind}>{result.content}</output>
}

describe('localized rich content', () => {
  afterEach(async () => {
    api.get = originalApiGet
    localStorage.removeItem('home_page_content')
    await i18n.changeLanguage('zh')
  })

  after(() => {
    domWindow.close()
  })

  test('reacts to language changes while rendering HTML and Markdown', async () => {
    const html = [
      '<tnt l="zh"><strong>中文页</strong></tnt>',
      '<tnt l="en"><strong>English page</strong></tnt>',
    ].join('')
    const markdown = [
      '<tnt l="zh">## 中文标题\n\n**重点内容**</tnt>',
      '<tnt l="en">## English title\n\n**Important content**</tnt>',
    ].join('')
    const rendered = await renderWithI18n(
      <>
        <RichContent className='html-boundary' mode='html' content={html} />
        <RichContent className='markdown-boundary' content={markdown} />
      </>
    )

    assert.equal(rendered.container.textContent?.includes('中文页'), true)
    assert.equal(rendered.container.textContent?.includes('中文标题'), true)
    assert.equal(
      rendered.container.querySelector('.markdown-boundary strong')
        ?.textContent,
      '重点内容'
    )
    assert.equal(rendered.container.textContent?.includes('<tnt'), false)

    await act(async () => i18n.changeLanguage('en'))

    assert.equal(rendered.container.textContent?.includes('English page'), true)
    assert.equal(
      rendered.container.textContent?.includes('English title'),
      true
    )
    assert.equal(rendered.container.textContent?.includes('中文页'), false)

    await unmountComponent(rendered)
  })

  test('classifies the localized home value instead of the raw multilingual source', async () => {
    const rawContent = [
      '<tnt l="zh">https://zh.example/home</tnt>',
      '<tnt l="en"><section>English home</section></tnt>',
    ].join('')
    let resolveRequest: ((value: unknown) => void) | undefined
    const request = new Promise((resolve) => {
      resolveRequest = resolve
    })
    api.get = (() => request) as unknown as typeof api.get
    const rendered = await renderWithI18n(<HomeContentProbe />)

    assert.equal(
      rendered.container.querySelector('output')?.dataset.kind,
      'loading'
    )

    await act(async () => {
      resolveRequest?.({ data: { success: true, data: rawContent } })
      await request
      await Promise.resolve()
    })

    const output = rendered.container.querySelector('output')
    assert.equal(output?.dataset.kind, 'url')
    assert.equal(output?.textContent, 'https://zh.example/home')
    assert.equal(localStorage.getItem('home_page_content'), rawContent)

    await act(async () => i18n.changeLanguage('en'))

    assert.equal(output?.dataset.kind, 'html')
    assert.equal(output?.textContent, '<section>English home</section>')

    await unmountComponent(rendered)
  })
})
