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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { ThemeCustomizationProvider, useThemeCustomization } =
  await import('../theme-customization-provider')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

function CustomizationProbe() {
  const { customization, resetCustomization } = useThemeCustomization()
  return (
    <button type='button' onClick={resetCustomization}>
      {customization.preset}:{customization.radius}
    </button>
  )
}

async function renderProvider() {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <ThemeCustomizationProvider>
        <CustomizationProbe />
      </ThemeCustomizationProvider>
    )
  })

  return { container, root }
}

function clearThemeCookies() {
  for (const name of ['theme_preset', 'theme_radius']) {
    document.cookie = `${name}=; path=/; max-age=0`
  }
}

describe('theme customization provider', () => {
  afterEach(() => {
    clearThemeCookies()
    document.body.replaceChildren()
    document.body.removeAttribute('data-theme-preset')
    document.body.removeAttribute('data-theme-radius')
  })

  after(() => {
    domWindow.close()
  })

  test('applies Tokeness defaults when no preference cookies exist', async () => {
    const rendered = await renderProvider()

    assert.equal(rendered.container.textContent, 'sunset-glow:none')
    assert.equal(document.body.dataset.themePreset, 'sunset-glow')
    assert.equal(document.body.dataset.themeRadius, 'none')

    await act(async () => rendered.root.unmount())
  })

  test('keeps explicit preset and radius cookies authoritative', async () => {
    document.cookie = 'theme_preset=default; path=/'
    document.cookie = 'theme_radius=xl; path=/'
    const rendered = await renderProvider()

    assert.equal(rendered.container.textContent, 'default:xl')
    assert.equal(document.body.hasAttribute('data-theme-preset'), false)
    assert.equal(document.body.dataset.themeRadius, 'xl')

    await act(async () => rendered.root.unmount())
  })

  test('reset removes custom cookies and reapplies Tokeness defaults', async () => {
    document.cookie = 'theme_preset=ocean-breeze; path=/'
    document.cookie = 'theme_radius=xl; path=/'
    const rendered = await renderProvider()

    await act(async () => {
      rendered.container.querySelector('button')?.click()
    })

    assert.equal(rendered.container.textContent, 'sunset-glow:none')
    assert.equal(document.body.dataset.themePreset, 'sunset-glow')
    assert.equal(document.body.dataset.themeRadius, 'none')
    assert.doesNotMatch(document.cookie, /(?:^|;\s*)theme_preset=[^;]/)
    assert.doesNotMatch(document.cookie, /(?:^|;\s*)theme_radius=[^;]/)

    await act(async () => rendered.root.unmount())
  })
})
