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

import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import vi from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'

const locales = { en, zh, 'zh-TW': zhTW, fr, ja, ru, vi }
const sidebarInvoiceKeys = ['Invoices', 'Invoice Review'] as const
const invoiceFormKeys = [
  'Invoice Type',
  'Individual',
  'Company',
  'Individual invoice reason is required',
  'Save Invoice Information',
  'Invoice information saved successfully',
  'Failed to load invoice information',
  'Failed to save invoice information',
] as const
const invoicePdfKeys = [
  'A real invoice PDF is required before marking the application as issued',
  'Real invoice PDF',
  'Not uploaded',
  'The uploaded PDF is sent immediately with the issued notification email and is not retained by the system.',
  'This application does not have a real invoice file yet',
] as const

// French-only accent characters that would reveal a wrong-language value.
const ACCENTED = /[À-ÖØ-öø-ÿ]/

function containsCJK(value: string): boolean {
  for (const ch of value) {
    if (ch >= '\u4e00' && ch <= '\u9fff') return true
  }
  return false
}

function assertTranslatedForEveryLocale(
  key: string,
  getLocaleValue: (locale: string, resource: Record<string, unknown>) => unknown
): void {
  for (const [locale, resource] of Object.entries(locales)) {
    const value = getLocaleValue(locale, resource)
    assert.equal(typeof value, 'string', `${locale} is missing ${key}`)
    const text = value as string
    assert.notEqual(text, '', `${locale} has an empty ${key}`)
    if (locale === 'en') continue
    const enTranslation = en.translation as Record<string, string>
    assert.notEqual(
      text,
      enTranslation[key],
      `${locale} falls back to the English ${key} label`
    )
    // The zh/zh-TW strings must be real Chinese, not another language.
    if (locale === 'zh' || locale === 'zh-TW') {
      assert.ok(containsCJK(text), `${locale} has no CJK for ${key}: ${text}`)
      assert.doesNotMatch(text, ACCENTED, `${locale} has accented text for ${key}: ${text}`)
    }
  }
}

describe('invoice sidebar localization', () => {
  test('defines translated user and admin invoice labels for every locale', () => {
    for (const [locale, resource] of Object.entries(locales)) {
      for (const key of sidebarInvoiceKeys) {
        const translation = resource.translation[key]

        assert.equal(typeof translation, 'string', `${locale} is missing ${key}`)
        assert.notEqual(translation, '', `${locale} has an empty ${key}`)
        if (locale !== 'en') {
          assert.notEqual(
            translation,
            en.translation[key],
            `${locale} falls back to the English ${key} label`
          )
        }
      }
    }
  })

  test('defines translated invoice form labels for every locale', () => {
    for (const [locale, resource] of Object.entries(locales)) {
      for (const key of invoiceFormKeys) {
        const translation = resource.translation[key]

        assert.equal(typeof translation, 'string', `${locale} is missing ${key}`)
        assert.notEqual(translation, '', `${locale} has an empty ${key}`)
        if (locale !== 'en') {
          assert.notEqual(
            translation,
            en.translation[key],
            `${locale} falls back to the English ${key} label`
          )
        }
      }
    }
  })

  test('defines translated real-invoice-PDF flow labels for every locale', () => {
    for (const key of invoicePdfKeys) {
      assertTranslatedForEveryLocale(key, (_locale, resource) => {
        const translation = resource.translation as Record<string, unknown>
        return translation[key]
      })
    }
  })
})
