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

import type { AuthUser } from '@/stores/auth-store'

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
  'HTMLButtonElement',
  'HTMLInputElement',
  'HTMLDivElement',
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

Object.defineProperty(domWindow, 'matchMedia', {
  configurable: true,
  value: () => ({
    matches: false,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }),
})

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { api } = await import('@/lib/api')
const { useAuthStore } = await import('@/stores/auth-store')
const { EmailBindingGate } = await import('../email-binding-gate')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: {
    en: {
      translation: {
        'An email address is required to continue.':
          'An email address is required to continue.',
        'Bind Email': 'Bind Email',
        'Email Address': 'Email Address',
        'Enter your email': 'Enter your email',
        'Verification Code': 'Verification Code',
        'Enter code': 'Enter code',
        Send: 'Send',
        Cancel: 'Cancel',
        'Email bound successfully!': 'Email bound successfully!',
        'Failed to bind email': 'Failed to bind email',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const missingEmailUser: AuthUser = {
  id: 1,
  username: 'oauth-user',
  role: 1,
}

async function renderGate() {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <I18nextProvider i18n={i18n}>
        <EmailBindingGate />
      </I18nextProvider>
    )
  })

  return { container, root }
}

function findButton(name: string): HTMLButtonElement | null {
  return (
    [...document.querySelectorAll('button')].find(
      (button) => button.textContent?.trim() === name
    ) ?? null
  )
}

function setInputValue(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value'
  )?.set
  setter?.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

afterEach(() => {
  useAuthStore.getState().auth.reset()
  document.body.replaceChildren()
})

after(() => {
  domWindow.close()
})

describe('required email binding', () => {
  test('blocks accounts without email after authentication and omits dismissal controls', async () => {
    useAuthStore.getState().auth.setUser(missingEmailUser)
    useAuthStore.getState().auth.setBootstrapState('complete')
    const rendered = await renderGate()

    assert.equal(
      document.body.textContent?.includes(
        'An email address is required to continue.'
      ),
      true
    )
    assert.equal(findButton('Cancel'), null)
    assert.equal(document.querySelector('[data-slot="dialog-close"]'), null)

    await act(async () => {
      useAuthStore.getState().auth.setUser({
        ...missingEmailUser,
        email: 'bound@example.com',
      })
    })

    assert.equal(document.querySelector('[data-slot="dialog-content"]'), null)
    await act(async () => rendered.root.unmount())
    rendered.container.remove()
  })

  test('does not open before authentication bootstrap completes', async () => {
    useAuthStore.getState().auth.setUser(missingEmailUser)
    useAuthStore.getState().auth.setBootstrapState('checking')
    const rendered = await renderGate()

    assert.equal(document.querySelector('[data-slot="dialog-content"]'), null)
    await act(async () => rendered.root.unmount())
    rendered.container.remove()
  })

  test('updates the authenticated user after successful binding', async () => {
    const originalAdapter = api.defaults.adapter
    try {
      api.defaults.adapter = async (config) => ({
        data: { success: true },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      })
      useAuthStore.getState().auth.setUser(missingEmailUser)
      useAuthStore.getState().auth.setBootstrapState('complete')
      const rendered = await renderGate()
      const emailInput = document.querySelector<HTMLInputElement>('#email')
      const codeInput = document.querySelector<HTMLInputElement>('#code')
      assert.ok(emailInput)
      assert.ok(codeInput)

      await act(async () => {
        setInputValue(emailInput, 'bound@example.com')
        setInputValue(codeInput, '123456')
      })

      const bindButton = findButton('Bind Email')
      assert.ok(bindButton)
      await act(async () => {
        bindButton.click()
        await Promise.resolve()
        await Promise.resolve()
      })

      assert.equal(
        useAuthStore.getState().auth.user?.email,
        'bound@example.com'
      )
      assert.equal(document.querySelector('[data-slot="dialog-content"]'), null)
      await act(async () => rendered.root.unmount())
      rendered.container.remove()
    } finally {
      api.defaults.adapter = originalAdapter
    }
  })
})
