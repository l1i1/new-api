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
import { describe, test } from 'node:test'

import { FORK_LOCALE_BUNDLES } from '../fork-bundles'
import {
  MAINLAND_OVERRIDE_KEYS,
  withMainlandCurrencyWording,
} from '../mainland-currency'

// Same composition as config.ts: the fork bundles already carry the overlay,
// and the mainland currency pass runs on top of them.
const resources = FORK_LOCALE_BUNDLES

const mainland = withMainlandCurrencyWording(resources)

const translationOf = (bundle: { translation: Record<string, string> }) =>
  bundle.translation

describe('mainland currency wording', () => {
  test('rewrites the platform currency words in every locale', () => {
    assert.equal(
      translationOf(mainland.zhCN)['Recharge Amount (USD)'],
      '充值金额 (CNY)'
    )
    assert.equal(
      translationOf(mainland.en)['Recharge Amount (USD)'],
      'Recharge Amount (CNY)'
    )
    assert.equal(
      translationOf(mainland.zhCN)['Minimum top-up (USD)'],
      '最低充值（人民币）'
    )
    assert.equal(
      translationOf(mainland.en)['Minimum top-up (USD)'],
      'Minimum top-up (CNY)'
    )
    assert.equal(
      translationOf(mainland.ja)['Price mode (USD per 1M tokens)'],
      '価格モード (100万トークンあたりのCNY)'
    )
    assert.equal(
      translationOf(mainland.ru)[
        'Cost in USD per request, regardless of tokens used.'
      ],
      'Стоимость в CNY за запрос, независимо от использованных токенов.'
    )
  })

  test('leaves no mainland-facing mention of the dollar in any locale', () => {
    const patterns: Record<keyof typeof resources, RegExp> = {
      en: /\bUSD\b|US dollars?|\bdollars\b/i,
      zhCN: /USD|美元/,
      zhTW: /USD|美元/,
      fr: /USD|dollar/i,
      ru: /USD|доллар/i,
      vi: /USD|đô la/i,
      ja: /USD|米ドル/,
    }

    for (const locale of Object.keys(patterns) as Array<
      keyof typeof resources
    >) {
      const pattern = patterns[locale]
      for (const [key, value] of Object.entries(
        translationOf(mainland[locale])
      )) {
        assert.ok(
          !pattern.test(value),
          `${locale}: ${JSON.stringify(key)} still says ${JSON.stringify(value)}`
        )
      }
    }
  })

  test('rewrites price notation symbols without corrupting examples', () => {
    assert.equal(
      translationOf(mainland.en)['Completion price ($/1M tokens)'],
      'Completion price (¥/1M tokens)'
    )
    assert.equal(
      translationOf(mainland.zhCN)[
        'Calculated price: ${{price}} per 1M tokens'
      ],
      '计算价格：¥{{price}} / 1M tokens'
    )
    assert.equal(
      translationOf(mainland.zhCN)['Custom Currency Symbol'],
      translationOf(resources.zhCN)['Custom Currency Symbol']
    )
    // Regex anchors and the HK$ placeholder must survive.
    const regexExample = 'e.g., gpt-4.1-nano,regex:^claude-.*$,regex:^sora-.*$'
    assert.equal(translationOf(mainland.en)[regexExample], regexExample)
    assert.equal(
      translationOf(mainland.zhCN)['e.g. ¥ or HK$'],
      '例如，¥ 或 HK$'
    )
  })

  test('applies hand-written overrides where mechanical rewriting breaks', () => {
    assert.equal(translationOf(mainland.zhCN)['CNY per USD'], '汇率')
    assert.equal(translationOf(mainland.en)['CNY per USD'], 'Exchange Rate')
    assert.equal(
      translationOf(mainland.zhCN)['US dollar (USD)'],
      '人民币 (CNY)'
    )
    assert.equal(
      translationOf(mainland.zhCN)[
        'Waffo Pancake USD exchange rate (CNY per USD)'
      ],
      'Waffo Pancake 汇率'
    )
    assert.equal(
      translationOf(mainland.zhCN)[
        'If CNY checkout is unavailable, WeChat Pay may charge the equivalent amount in USD.'
      ],
      '如果人民币结账不可用，微信支付可能改为按等值外币扣款。'
    )
    assert.equal(translationOf(mainland.zhCN)['CNY'], '人民币')
  })

  test('every override key exists in the base English locale', () => {
    for (const key of MAINLAND_OVERRIDE_KEYS) {
      assert.ok(
        key in translationOf(resources.en),
        `override key missing: ${key}`
      )
    }
  })

  test('does not rewrite unrelated wording', () => {
    // バンドル and コードルール contain ドル but are not dollar words.
    const bundleKey = 'Capture a reusable bundle of models, tags, or endpoints.'
    assert.equal(
      translationOf(mainland.ja)[bundleKey],
      translationOf(resources.ja)[bundleKey]
    )
    const ruleKey = 'Invalid status code rules: {{tokens}}'
    assert.equal(
      translationOf(mainland.ja)[ruleKey],
      translationOf(resources.ja)[ruleKey]
    )
    assert.equal(translationOf(mainland.en)['Dashboard'], 'Dashboard')
  })

  test('returns a copy and leaves the base resources untouched', () => {
    assert.notEqual(mainland, resources)
    assert.notEqual(mainland.zhCN.translation, translationOf(resources.zhCN))
    assert.equal(
      translationOf(resources.zhCN)['Recharge Amount (USD)'],
      '充值金额 (USD)'
    )
    assert.equal(
      translationOf(resources.en)['Recharge Amount (USD)'],
      'Recharge Amount (USD)'
    )
  })
})
