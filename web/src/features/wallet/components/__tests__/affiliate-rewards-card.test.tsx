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

import type { InviteTopUpRewardsData, UserWalletData } from '../../types'

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
const { useSystemConfigStore } = await import('@/stores/system-config-store')
const { AffiliateRewardsCard } = await import('../affiliate-rewards-card')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: { en: { translation: {} } },
})

const originalCurrencyConfig = {
  ...useSystemConfigStore.getState().config.currency,
}
const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const user: UserWalletData = {
  id: 1,
  username: 'test-user',
  quota: 1000,
  used_quota: 500,
  request_count: 10,
  aff_quota: 100,
  aff_history_quota: 250,
  aff_count: 3,
  group: 'default',
}

const rewards: InviteTopUpRewardsData = {
  program_enabled: true,
  reward_rate_bps: 2000,
  summary: {
    applied_count: 2,
    pending_count: 1,
    skipped_count: 1,
    total_reward_quota: 750000,
  },
  items: [
    {
      id: 3,
      reward_quota: 500000,
      status: 'applied',
      created_at: 1_700_000_000,
      applied_at: 1_700_000_100,
    },
    {
      id: 2,
      reward_quota: 250000,
      status: 'pending',
      created_at: 1_699_999_000,
      applied_at: 0,
    },
    {
      id: 1,
      reward_quota: 100000,
      status: 'skipped',
      created_at: 1_699_998_000,
      applied_at: 0,
    },
  ],
  page: 1,
  page_size: 5,
  total: 4,
}

type RenderedCard = {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}

async function renderCard(
  props: Partial<React.ComponentProps<typeof AffiliateRewardsCard>> = {}
): Promise<RenderedCard> {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <I18nextProvider i18n={i18n}>
        <AffiliateRewardsCard
          user={user}
          affiliateLink='https://tokeness.test/register?aff=code'
          {...props}
        />
      </I18nextProvider>
    )
  })

  return { container, root }
}

async function unmountCard(rendered: RenderedCard) {
  await act(async () => rendered.root.unmount())
  rendered.container.remove()
}

describe('affiliate rewards card', () => {
  afterEach(() => {
    useSystemConfigStore.getState().setConfig({
      currency: originalCurrencyConfig,
    })
  })

  after(() => {
    domWindow.close()
  })

  test('shows ledger-backed summary and recent statuses without restoring legacy transfer balances', async () => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...originalCurrencyConfig,
        quotaDisplayType: 'USD',
        quotaPerUnit: 500000,
        usdExchangeRate: 7,
      },
    })
    const rendered = await renderCard({ rewards })
    const text = rendered.container.textContent ?? ''

    assert.match(text, /20%/)
    assert.match(text, /Invites/)
    assert.match(text, /Completed first top-ups/)
    assert.match(text, /Total earned/)
    assert.match(text, /Processing/)
    assert.match(text, /Received/)
    assert.match(text, /Not issued/)
    assert.match(text, /¥10\.5/)
    assert.match(text, /\+¥7/)
    assert.doesNotMatch(text, /Transfer to Balance/)
    assert.doesNotMatch(text, /Affiliate quota/)
    assert.equal(rendered.container.querySelectorAll('button').length, 1)
    assert.equal(
      rendered.container.querySelector('button')?.getAttribute('aria-label'),
      'Copy referral link'
    )

    const summary = rendered.container.querySelector(
      '[aria-label="Referral reward summary"]'
    )
    assert.ok(summary)
    assert.match(summary.className, /grid-cols-2/)
    assert.match(summary.className, /sm:grid-cols-4/)
    assert.match(summary.className, /lg:grid-cols-2/)
    assert.match(summary.className, /xl:grid-cols-4/)
    assert.match(
      rendered.container.querySelector('section[aria-labelledby]')?.className ??
        '',
      /lg:border-l/
    )

    await unmountCard(rendered)
  })

  test('keeps the referral link usable when reward data fails and provides a retry action', async () => {
    let retries = 0
    const rendered = await renderCard({
      rewardsError: true,
      onRetryRewards: () => {
        retries += 1
      },
    })

    assert.match(
      rendered.container.textContent ?? '',
      /Reward data is temporarily unavailable/
    )
    const buttons = [...rendered.container.querySelectorAll('button')]
    assert.equal(buttons.length, 2)
    const copyButton = buttons.find(
      (button) => button.getAttribute('aria-label') === 'Copy referral link'
    )
    const retryButton = buttons.find((button) => button.textContent === 'Retry')
    assert.ok(copyButton)
    assert.ok(retryButton)

    await act(async () => retryButton.click())
    assert.equal(retries, 1)

    await unmountCard(rendered)
  })

  test('renders distinct loading and empty reward states inside the existing card', async () => {
    const loading = await renderCard({ rewardsLoading: true })
    assert.ok(loading.container.querySelector('[aria-label="Loading rewards"]'))
    assert.doesNotMatch(loading.container.textContent ?? '', /20%/)
    assert.doesNotMatch(
      loading.container.textContent ?? '',
      /No first top-up rewards yet/
    )
    await unmountCard(loading)

    const empty = await renderCard({
      rewards: {
        ...rewards,
        summary: {
          applied_count: 0,
          pending_count: 0,
          skipped_count: 0,
          total_reward_quota: 0,
        },
        items: [],
        total: 0,
      },
    })
    assert.match(
      empty.container.textContent ?? '',
      /No first top-up rewards yet/
    )
    assert.equal(empty.container.querySelectorAll('button').length, 1)
    await unmountCard(empty)
  })

  test('degrades safely for unknown statuses and invalid timestamps', async () => {
    const rendered = await renderCard({
      rewards: {
        ...rewards,
        items: [
          {
            id: 99,
            reward_quota: 100,
            status: 'future-status' as never,
            created_at: Number.MAX_VALUE,
            applied_at: 0,
          },
        ],
        total: 1,
      },
    })

    assert.match(rendered.container.textContent ?? '', /Not issued/)
    assert.doesNotMatch(rendered.container.textContent ?? '', /Invalid Date/)

    await unmountCard(rendered)
  })

  test('explains a paused program without hiding historical reward data or the referral link', async () => {
    const rendered = await renderCard({
      rewards: { ...rewards, program_enabled: false },
    })

    assert.match(
      rendered.container.textContent ?? '',
      /First top-up rewards are currently paused/
    )
    assert.match(rendered.container.textContent ?? '', /Received/)
    assert.equal(
      rendered.container.querySelector('button')?.getAttribute('aria-label'),
      'Copy referral link'
    )

    await unmountCard(rendered)
  })
})
