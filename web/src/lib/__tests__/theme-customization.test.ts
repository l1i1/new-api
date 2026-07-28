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

import { initializeThemeCustomizationDom } from '../theme-customization-storage'

const domWindow = new Window({ url: 'https://tokeness.test/' })
Object.defineProperty(globalThis, 'document', {
  configurable: true,
  value: domWindow.document,
})

describe('Tokeness theme defaults', () => {
  afterEach(() => {
    document.cookie = 'theme_preset=; path=/; max-age=0'
    document.cookie = 'theme_radius=; path=/; max-age=0'
    document.body.removeAttribute('data-theme-preset')
    document.body.removeAttribute('data-theme-radius')
  })

  after(() => {
    domWindow.close()
  })

  test('applies defaults before React mounts while preserving valid cookies', () => {
    const defaults = initializeThemeCustomizationDom()
    assert.equal(defaults.preset, 'sunset-glow')
    assert.equal(defaults.radius, 'none')
    assert.equal(document.body.dataset.themePreset, 'sunset-glow')
    assert.equal(document.body.dataset.themeRadius, 'none')

    document.cookie = 'theme_preset=ocean-breeze; path=/'
    document.cookie = 'theme_radius=xl; path=/'
    const stored = initializeThemeCustomizationDom()
    assert.equal(stored.preset, 'ocean-breeze')
    assert.equal(stored.radius, 'xl')
    assert.equal(document.body.dataset.themePreset, 'ocean-breeze')
    assert.equal(document.body.dataset.themeRadius, 'xl')
  })
})
