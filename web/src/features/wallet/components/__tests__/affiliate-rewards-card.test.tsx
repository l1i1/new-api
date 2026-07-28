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

import { Window } from 'happy-dom'

const domWindow = new Window({ url: 'https://tokeness.test/wallet' })
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
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { AffiliateRewardsCard } = await import('../affiliate-rewards-card')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

describe('affiliate rewards card', () => {
  after(() => {
    domWindow.close()
  })

  test('keeps the referral explanation, statistics, and compliant transfer action available', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <AffiliateRewardsCard
            user={{
              id: 1,
              username: 'test-user',
              quota: 1000,
              used_quota: 500,
              request_count: 10,
              aff_quota: 100,
              aff_history_quota: 250,
              aff_count: 3,
              group: 'default',
            }}
            affiliateLink='https://tokeness.test/register?aff=code'
            onTransfer={() => undefined}
          />
        </I18nextProvider>
      )
    })

    assert.match(
      container.textContent ?? '',
      /Earn rewards when users join through your referral link\. Transfer accumulated rewards to your balance anytime\./
    )
    assert.match(container.textContent ?? '', /Pending/)
    assert.match(container.textContent ?? '', /Total Earned/)
    assert.match(container.textContent ?? '', /Invites/)
    assert.equal(
      container.querySelector<HTMLButtonElement>(
        'button:not([aria-label="Copy referral link"])'
      )?.textContent,
      'Transfer to Balance'
    )

    await act(async () => root.unmount())
    container.remove()
  })
})
