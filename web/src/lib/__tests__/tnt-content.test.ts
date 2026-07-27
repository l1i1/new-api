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

import { normalizeTntContentLanguage, resolveTntContent } from '../tnt-content'

describe('Tokeness authored content localization', () => {
  test('normalizes supported regional and interface language values', () => {
    assert.equal(normalizeTntContentLanguage('zh-TW'), 'zh')
    assert.equal(normalizeTntContentLanguage('zhCN'), 'zh')
    assert.equal(normalizeTntContentLanguage('fr-FR'), 'fr')
    assert.equal(normalizeTntContentLanguage('en_US'), 'en')
    assert.equal(normalizeTntContentLanguage('de-DE'), null)
  })

  test('selects each supported active language from one adjacent group', () => {
    const content = [
      '<tnt l="zh">ZH</tnt>',
      '<tnt l="en">EN</tnt>',
      '<tnt l="fr">FR</tnt>',
      '<tnt l="ru">RU</tnt>',
      '<tnt l="ja">JA</tnt>',
      '<tnt l="vi">VI</tnt>',
    ].join('\n')

    assert.equal(resolveTntContent(content, 'zh-TW'), 'ZH')
    assert.equal(resolveTntContent(content, 'en'), 'EN')
    assert.equal(resolveTntContent(content, 'fr'), 'FR')
    assert.equal(resolveTntContent(content, 'ru'), 'RU')
    assert.equal(resolveTntContent(content, 'ja'), 'JA')
    assert.equal(resolveTntContent(content, 'vi'), 'VI')
  })

  test('falls back to English and then Chinese', () => {
    const englishFallback =
      '<tnt l="zh">ZH</tnt><tnt l="en">EN</tnt><tnt l="fr">FR</tnt>'
    const chineseFallback = '<tnt l="zh">ZH</tnt><tnt l="fr">FR</tnt>'

    assert.equal(resolveTntContent(englishFallback, 'ja'), 'EN')
    assert.equal(resolveTntContent(chineseFallback, 'ja'), 'ZH')
  })

  test('resolves adjacent groups independently when ordinary content separates them', () => {
    const content =
      '# <tnt l="zh">标题</tnt>\n<tnt l="en">Title</tnt>\n\n' +
      '<section><tnt l="zh"><strong>正文</strong></tnt> ' +
      '<tnt l="en"><strong>Body</strong></tnt></section>'

    assert.equal(
      resolveTntContent(content, 'en-US'),
      '# Title\n\n<section><strong>Body</strong></section>'
    )
  })

  test('preserves malformed or ambiguous groups without partially translating them', () => {
    const invalidGroups = [
      '<tnt>Missing language</tnt><tnt l="en">English</tnt>',
      '<tnt l="de">Deutsch</tnt><tnt l="en">English</tnt>',
      '<tnt l="en">First</tnt><tnt l="en">Second</tnt>',
      '<tnt l="en">Outer <tnt l="zh">Inner</tnt></tnt>',
      '<tnt l="en">Unclosed',
    ]

    for (const content of invalidGroups) {
      assert.equal(resolveTntContent(content, 'en'), content)
    }
  })

  test('does not treat JSON language keys or the legacy tag as localization', () => {
    const json = '{"en":"English","zh":"中文"}'
    const legacy =
      '<tokeness-text lang="zh">中文</tokeness-text>' +
      '<tokeness-text lang="en">English</tokeness-text>'

    assert.equal(resolveTntContent(json, 'en'), json)
    assert.equal(resolveTntContent(legacy, 'en'), legacy)
  })

  test('is idempotent after a language has been selected', () => {
    const content = '<p><tnt l="zh">你好</tnt><tnt l="en">Hello</tnt></p>'
    const localized = resolveTntContent(content, 'en')

    assert.equal(resolveTntContent(localized, 'en'), localized)
  })
})
