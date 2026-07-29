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

import type { PaymentMethod, TopupInfo } from '../../types'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLButtonElement',
  'HTMLDivElement',
  'HTMLFormElement',
  'HTMLInputElement',
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
  'ShadowRoot',
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

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  fallbackLng: 'en',
  resources: {
    en: {
      translation: {
        Save: 'Save',
        'You save': 'You save',
      },
    },
    zh: {
      translation: {
        Save: '保存',
        'You save': '节省',
      },
    },
  },
})

const { PaymentConfirmDialog } =
  await import('../dialogs/payment-confirm-dialog')
const { RechargeFormCard } = await import('../recharge-form-card')
const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

type RenderedComponent = {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}

async function renderComponent(
  node: React.ReactNode
): Promise<RenderedComponent> {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>)
  })

  return { container, root }
}

async function unmountComponent(rendered: RenderedComponent) {
  await act(async () => rendered.root.unmount())
  rendered.container.remove()
}

const epayMethod: PaymentMethod = {
  name: '<tnt l="zh">支付宝</tnt><tnt l="en">Alipay</tnt>',
  type: 'alipay',
  min_topup: 1,
}

const topupInfo: TopupInfo = {
  enable_online_topup: true,
  enable_stripe_topup: false,
  pay_methods: [epayMethod],
  min_topup: 1,
  stripe_min_topup: 1,
  amount_options: [10],
  discount: { 10: 0.8 },
  enable_redemption: true,
}

describe('wallet payment surfaces', () => {
  after(() => {
    domWindow.close()
  })

  test('uses the user-facing savings label and preserves the selected payment payload', async () => {
    const selectedMethods: PaymentMethod[] = []
    const rendered = await renderComponent(
      <RechargeFormCard
        topupInfo={topupInfo}
        presetAmounts={[{ value: 10, discount: 0.8 }]}
        selectedPreset={10}
        onSelectPreset={() => undefined}
        topupAmount={10}
        onTopupAmountChange={() => undefined}
        paymentAmount={8}
        calculating={false}
        onPaymentMethodSelect={(method) => selectedMethods.push(method)}
        paymentLoading={null}
        redemptionCode=''
        onRedemptionCodeChange={() => undefined}
        onRedeem={() => undefined}
        redeeming={false}
        priceRatio={7}
      />
    )

    assert.equal(rendered.container.textContent?.includes('You save'), true)
    assert.equal(rendered.container.textContent?.includes('<tnt'), false)
    assert.equal(rendered.container.textContent?.includes('×7 CNY ='), true)
    assert.match(
      rendered.container.querySelector('[data-testid="wallet-payment-amount"]')
        ?.textContent ?? '',
      /8/
    )

    const paymentButton = rendered.container.querySelector(
      'button[aria-label="Alipay"]'
    )
    assert.ok(paymentButton instanceof HTMLButtonElement)
    await act(async () => paymentButton.click())
    assert.deepEqual(selectedMethods, [epayMethod])

    await unmountComponent(rendered)
  })

  test('shows the payable row and redirect warning only for standard EPay methods', async () => {
    const warning =
      'Do not close the payment page after paying. Wait to be redirected back automatically.'
    const rendered = await renderComponent(
      <PaymentConfirmDialog
        open
        onOpenChange={() => undefined}
        onConfirm={() => undefined}
        topupAmount={10}
        paymentAmount={8}
        paymentMethod={epayMethod}
        calculating={false}
        processing={false}
      />
    )

    assert.equal(document.body.textContent?.includes(warning), true)
    assert.equal(document.body.textContent?.includes('You Pay'), true)

    await act(async () => {
      rendered.root.render(
        <I18nextProvider i18n={i18n}>
          <PaymentConfirmDialog
            open
            onOpenChange={() => undefined}
            onConfirm={() => undefined}
            topupAmount={10}
            paymentAmount={8}
            paymentMethod={{ name: 'Stripe', type: 'stripe' }}
            calculating={false}
            processing={false}
          />
        </I18nextProvider>
      )
    })

    assert.equal(document.body.textContent?.includes(warning), false)
    assert.equal(document.body.textContent?.includes('You Pay'), false)

    await act(async () => {
      rendered.root.render(
        <I18nextProvider i18n={i18n}>
          <PaymentConfirmDialog
            open
            onOpenChange={() => undefined}
            onConfirm={() => undefined}
            topupAmount={10}
            paymentAmount={8}
            paymentMethod={{ name: 'Waffo', type: 'waffo' }}
            calculating={false}
            processing={false}
          />
        </I18nextProvider>
      )
    })

    assert.equal(document.body.textContent?.includes(warning), false)
    assert.equal(document.body.textContent?.includes('You Pay'), false)

    for (const paymentMethod of [
      { name: 'Waffo Pancake', type: 'waffo_pancake' },
      { name: 'Creem', type: 'creem' },
    ]) {
      await act(async () => {
        rendered.root.render(
          <I18nextProvider i18n={i18n}>
            <PaymentConfirmDialog
              open
              onOpenChange={() => undefined}
              onConfirm={() => undefined}
              topupAmount={10}
              paymentAmount={8}
              paymentMethod={paymentMethod}
              calculating={false}
              processing={false}
            />
          </I18nextProvider>
        )
      })

      assert.equal(document.body.textContent?.includes(warning), false)
      assert.equal(document.body.textContent?.includes('You Pay'), false)
    }

    await unmountComponent(rendered)
  })

  test('uses the Chinese multiplier form without calculating the payable amount locally', async () => {
    await act(async () => {
      await i18n.changeLanguage('zh')
    })
    const rendered = await renderComponent(
      <RechargeFormCard
        topupInfo={topupInfo}
        presetAmounts={[]}
        selectedPreset={null}
        onSelectPreset={() => undefined}
        topupAmount={10}
        onTopupAmountChange={() => undefined}
        paymentAmount={61.5}
        calculating={false}
        onPaymentMethodSelect={() => undefined}
        paymentLoading={null}
        redemptionCode=''
        onRedemptionCodeChange={() => undefined}
        onRedeem={() => undefined}
        redeeming={false}
        priceRatio={7}
      />
    )

    assert.equal(rendered.container.textContent?.includes('×¥7='), true)
    assert.match(
      rendered.container.querySelector('[data-testid="wallet-payment-amount"]')
        ?.textContent ?? '',
      /61\.5/
    )
    assert.doesNotMatch(
      rendered.container.querySelector('[data-testid="wallet-payment-amount"]')
        ?.textContent ?? '',
      /70/
    )

    await unmountComponent(rendered)
    await act(async () => {
      await i18n.changeLanguage('en')
    })
  })
})
