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
/**
 * ============================================================================
 * Mainland currency wording
 * ============================================================================
 *
 * The mainland edition accounts in RMB: the site exchange rate is pinned to 1,
 * display is always CNY, and 500000 quota is one yuan. Upstream copy still
 * names the internal unit "USD" / "美元" everywhere, which reads as if the
 * platform billed in dollars. This module rewrites those wordings to CNY for
 * the mainland bundle — and only there: the overseas bundle never runs it.
 *
 * Two layers, in this order:
 *
 * 1. Term rules — mechanical per-locale replacements of the currency words
 *    (`USD`, `美元`, `米ドル`, `US dollar`, …) plus the price-prefix symbols.
 *    They cover every translated string, so a newly added upstream string that
 *    says "USD" is reworded without touching this file.
 * 2. Overrides — whole-value replacements for the few keys whose mechanical
 *    result would be broken or false ("人民币兑人民币汇率", "CNY (CNY)",
 *    "Pancake CNY", …). Keys must exist in the base locale files; the unit test
 *    enforces that so an upstream rename cannot silently disable one.
 */
export type WordingResources = Record<
  string,
  { translation: Record<string, string> }
>

type TermRule = readonly [pattern: RegExp, replacement: string]

/**
 * Words that name the platform's own account unit in each shipped locale.
 * Order matters: the longer phrase must win before the bare code does.
 */
const LOCALE_TERM_RULES: Record<string, readonly TermRule[]> = {
  en: [
    [/\bUS dollars\b/g, 'CNY'],
    [/\bUS dollar\b/g, 'CNY'],
    [/\bUSD\b/g, 'CNY'],
    [/\bdollars\b/g, 'CNY'],
  ],
  zhCN: [
    [/美元/g, '人民币'],
    [/USD/g, 'CNY'],
  ],
  zhTW: [
    [/美元/g, '人民幣'],
    [/USD/g, 'CNY'],
  ],
  // ドル alone is not a safe match: it also occurs inside バンドル (bundle) and
  // コードルール (code rule), so exclude those two preceding sequences.
  ja: [
    [/米ドル/g, '人民元'],
    [/(?<!バン)(?<!ー)ドル/g, '人民元'],
    [/USD/g, 'CNY'],
    [/(?<=あたり)\$/g, '¥'],
  ],
  fr: [
    [/[Dd]ollars? US/g, 'CNY'],
    [/[Dd]ollars? américains?/g, 'CNY'],
    [/\bdollars?\b/g, 'CNY'],
    [/USD/g, 'CNY'],
  ],
  ru: [
    [/долларах США/g, 'CNY'],
    [/долларов США/g, 'CNY'],
    [/доллар США/g, 'CNY'],
    // Cyrillic letters are not `\w`, so a word boundary would never match here.
    [/доллар\p{L}*/gu, 'CNY'],
    [/USD/g, 'CNY'],
  ],
  vi: [
    [/[Đđ]ô la Mỹ/g, 'CNY'],
    [/USD/g, 'CNY'],
  ],
}

/**
 * Currency symbols, applied to every locale. A blanket `$` -> `¥` would corrupt
 * regex examples (`regex:^claude-.*$`) and the custom-symbol placeholder
 * (`e.g. ¥ or HK$`), so only the price notations are rewritten.
 */
const SYMBOL_RULES: readonly TermRule[] = [
  [/\$(?=\/\d)/g, '¥'], // $/1M, $/1K, $/1 млн
  [/\$(?=\d)/g, '¥'], // $1 / $5 / $10
  [/\$\{\{/g, '¥{{'], // ${{price}}, ${{amount}}
]

type LocaleCode = 'en' | 'zhCN' | 'zhTW' | 'ja' | 'fr' | 'ru' | 'vi'

/** Whole-value replacements for keys the term rules cannot rewrite cleanly. */
const OVERRIDES: Record<string, Partial<Record<LocaleCode, string>>> = {
  // Term rules would produce "CNY per CNY" / "人民币兑人民币汇率". Used as the
  // exchange-rate field label, which is pinned to 1 on the mainland site.
  'CNY per USD': {
    en: 'Exchange Rate',
    zhCN: '汇率',
    zhTW: '匯率',
    ja: '為替レート',
    fr: 'Taux de change',
    ru: 'Обменный курс',
    vi: 'Tỷ giá',
  },
  'USD Exchange Rate': {
    en: 'Exchange Rate',
    zhCN: '汇率',
    zhTW: '匯率',
    ja: '為替レート',
    fr: 'Taux de change',
    ru: 'Обменный курс',
    vi: 'Tỷ giá',
  },
  // "US dollar (USD)" would become "CNY (CNY)".
  'US dollar (USD)': {
    en: 'CNY',
    zhCN: '人民币 (CNY)',
    zhTW: '人民幣 (CNY)',
    ja: '人民元 (CNY)',
    fr: 'CNY',
    ru: 'CNY',
    vi: 'CNY',
  },
  // Mechanical result: "Waffo Pancake CNY exchange rate (CNY per CNY)".
  'Waffo Pancake USD exchange rate (CNY per USD)': {
    en: 'Waffo Pancake exchange rate',
    zhCN: 'Waffo Pancake 汇率',
    zhTW: 'Waffo Pancake 匯率',
    ja: 'Waffo Pancake の為替レート',
    fr: 'Taux de change Waffo Pancake',
    ru: 'Обменный курс Waffo Pancake',
    vi: 'Tỷ giá Waffo Pancake',
  },
  // The gateway settles in its own currency, so renaming it to CNY would state
  // something false; the mainland copy just drops the currency name.
  'Used to convert the local CNY payable amount into Pancake USD. Leave it at the recharge price to keep a one-to-one platform USD conversion.':
    {
      en: 'Used to convert the local CNY payable amount into the Pancake settlement amount. Leave it at the recharge price to keep a one-to-one platform conversion.',
      zhCN: '用于将本地人民币应付金额转换为 Pancake 的结算金额。保持与充值价格一致，即可按平台计价一比一换算。',
    },
  // "If CNY checkout is unavailable, WeChat Pay may charge the equivalent
  // amount in CNY" would be a tautology; the provider falls back to a currency
  // that is foreign to a mainland payer.
  'If CNY checkout is unavailable, WeChat Pay may charge the equivalent amount in USD.':
    {
      en: 'If CNY checkout is unavailable, WeChat Pay may charge the equivalent amount in another currency.',
      zhCN: '如果人民币结账不可用，微信支付可能改为按等值外币扣款。',
      zhTW: '如果人民幣結帳不可用，微信支付可能改為按等值外幣扣款。',
    },
  // Shown as the selected display currency on the billing settings page; the
  // platform's own name for its unit beats the bare ISO code in Chinese.
  CNY: {
    zhCN: '人民币',
    zhTW: '人民幣',
  },
}

/** English source keys that carry a mainland override, exported for the test. */
export const MAINLAND_OVERRIDE_KEYS = Object.keys(OVERRIDES)

function rewriteCurrencyTerms(value: string, locale: string): string {
  let next = value
  for (const [pattern, replacement] of LOCALE_TERM_RULES[locale] ?? []) {
    next = next.replace(pattern, replacement)
  }
  for (const [pattern, replacement] of SYMBOL_RULES) {
    next = next.replace(pattern, replacement)
  }
  return next
}

/**
 * Return a copy of the i18next resources carrying the mainland currency
 * wording. Only the translation maps are rewritten; every locale and key that
 * the base files ship stays in place, and no key is added.
 */
export function withMainlandCurrencyWording<T extends WordingResources>(
  resources: T
): T {
  const rewritten: WordingResources = {}

  for (const [locale, bundle] of Object.entries(resources)) {
    const translation: Record<string, string> = {}

    for (const [key, value] of Object.entries(bundle.translation)) {
      translation[key] = OVERRIDES[key]?.[locale as LocaleCode] ?? value
      if (translation[key] === value) {
        translation[key] = rewriteCurrencyTerms(value, locale)
      }
    }

    rewritten[locale] = { ...bundle, translation }
  }

  return rewritten as T
}
