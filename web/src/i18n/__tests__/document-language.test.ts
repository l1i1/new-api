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

import { convertDetectedLanguage, toDocumentLanguage } from '../languages'

describe('document language mapping', () => {
  test('returns valid BCP-47 tags for every supported interface language', () => {
    assert.equal(toDocumentLanguage('en'), 'en')
    assert.equal(toDocumentLanguage('fr'), 'fr')
    assert.equal(toDocumentLanguage('ru'), 'ru')
    assert.equal(toDocumentLanguage('ja'), 'ja')
    assert.equal(toDocumentLanguage('vi'), 'vi')
    assert.equal(toDocumentLanguage('zhCN'), 'zh-CN')
    assert.equal(toDocumentLanguage('zhTW'), 'zh-TW')
  })

  test('falls back to English for unsupported language values', () => {
    assert.equal(toDocumentLanguage('invalid'), 'en')
    assert.equal(toDocumentLanguage(undefined), 'en')
  })

  test('preserves the cached Traditional Chinese interface code', () => {
    assert.equal(convertDetectedLanguage('zhTW'), 'zhTW')
  })
})
