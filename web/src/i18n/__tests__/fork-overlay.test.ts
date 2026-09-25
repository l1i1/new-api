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
import en from '../locales/en.json'
import fr from '../locales/fr.json'
import ja from '../locales/ja.json'
import ru from '../locales/ru.json'
import vi from '../locales/vi.json'
import zhTW from '../locales/zh-TW.json'
import zh from '../locales/zh.json'
import { FORK_LOCALE_OVERLAY, RESOURCE_TO_OVERLAY } from '../overlay'

/** Upstream-owned bundle per overlay key (the file the overlay is applied to). */
const UPSTREAM_BUNDLES: Record<
  string,
  { translation: Record<string, string> }
> = {
  en,
  zh,
  'zh-TW': zhTW,
  fr,
  ru,
  ja,
  vi,
}

const overlayKeys = Object.keys(FORK_LOCALE_OVERLAY).sort()
const resourceKeys = Object.values(RESOURCE_TO_OVERLAY).sort()

describe('fork i18n overlay', () => {
  test('maps every i18next resource key onto an overlay file', () => {
    assert.deepEqual(resourceKeys, overlayKeys)
    for (const locale of resourceKeys) {
      assert.ok(FORK_LOCALE_OVERLAY[locale], `missing overlay for ${locale}`)
      assert.ok(
        UPSTREAM_BUNDLES[locale],
        `missing upstream bundle for ${locale}`
      )
    }
  })

  test('all locales carry the same fork-only key set', () => {
    // Fork-only keys are new UI, so every locale must ship them; a key missing
    // from one locale silently falls back to the raw key / English there.
    // (`overrides` are per-locale by nature: a locale only needs an entry where
    // its value differs from the upstream bundle.)
    const reference = overlayKeys[0]
    const referenceTranslation = Object.keys(
      FORK_LOCALE_OVERLAY[reference].translation
    ).sort()

    assert.ok(referenceTranslation.length > 0, 'overlay is empty')

    for (const locale of overlayKeys) {
      assert.deepEqual(
        Object.keys(FORK_LOCALE_OVERLAY[locale].translation).sort(),
        referenceTranslation,
        `${locale} fork-only key set differs from ${reference}`
      )
    }
  })

  test('overrides still differ from the upstream bundle', () => {
    // A no-op override means upstream adopted our wording (or the entry is a
    // leftover); either way it should be deleted instead of lingering.
    for (const locale of overlayKeys) {
      const upstream = UPSTREAM_BUNDLES[locale].translation
      for (const [key, value] of Object.entries(
        FORK_LOCALE_OVERLAY[locale].overrides
      )) {
        assert.ok(key in upstream, `${locale}: override ${key} is not upstream`)
        assert.notEqual(
          upstream[key],
          value,
          `${locale}: override ${key} no longer differs from upstream`
        )
      }
    }
  })

  test('a key is either fork-only or an override, never both', () => {
    for (const locale of overlayKeys) {
      const { translation, overrides } = FORK_LOCALE_OVERLAY[locale]
      const both = Object.keys(translation).filter((key) => key in overrides)
      assert.deepEqual(
        both,
        [],
        `${locale} declares ${both.length} key(s) twice`
      )
      for (const [key, value] of Object.entries({
        ...translation,
        ...overrides,
      })) {
        assert.notEqual(key.trim(), '', `${locale} has an empty key`)
        assert.equal(typeof value, 'string', `${locale}.${key} is not a string`)
        assert.notEqual(value, '', `${locale}.${key} is empty`)
      }
    }
  })

  test('fork-only keys do not exist upstream yet', () => {
    // The upstream bundles are upstream-owned bytes. A fork-only key showing up
    // in one of them means upstream shipped the same key: decide whether to
    // drop the fork entry (upstream wins) or move it to `overrides`.
    for (const locale of overlayKeys) {
      const upstream = UPSTREAM_BUNDLES[locale].translation
      const collisions = Object.keys(
        FORK_LOCALE_OVERLAY[locale].translation
      ).filter((key) => key in upstream)
      assert.deepEqual(
        collisions,
        [],
        `${locale}: upstream now ships ${collisions.length} fork-only key(s); resolve in src/i18n/overlay/${locale}.json`
      )
    }
  })

  test('override keys still exist upstream', () => {
    // A stale override silently does nothing (or worse, hides that upstream
    // renamed the key), so it has to be cleaned up with the sync.
    for (const locale of overlayKeys) {
      const upstream = UPSTREAM_BUNDLES[locale].translation
      const stale = Object.keys(FORK_LOCALE_OVERLAY[locale].overrides).filter(
        (key) => !(key in upstream)
      )
      assert.deepEqual(
        stale,
        [],
        `${locale}: override(s) no longer exist upstream; drop them from src/i18n/overlay/${locale}.json`
      )
    }
  })

  test('composed bundles carry the overlay', () => {
    for (const [resource, overlayKey] of Object.entries(RESOURCE_TO_OVERLAY)) {
      const bundle =
        FORK_LOCALE_BUNDLES[resource as keyof typeof FORK_LOCALE_BUNDLES]
      const { translation, overrides } = FORK_LOCALE_OVERLAY[overlayKey]
      for (const [key, value] of Object.entries({
        ...translation,
        ...overrides,
      })) {
        assert.equal(
          bundle.translation[key],
          value,
          `${resource} does not expose overlay key ${key}`
        )
      }
    }
  })
})
