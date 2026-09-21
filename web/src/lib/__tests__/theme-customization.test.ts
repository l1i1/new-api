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
// The storage helpers read window.localStorage. Install the fixture window
// only inside this file's test and remove it again, because every node:test
// file shares one process: a window/localStorage pair that leaks out of here
// changes how the other files take their environment branches.
function installStorageGlobals() {
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: domWindow,
  })
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: domWindow.localStorage,
  })
}

function removeStorageGlobals() {
  Reflect.deleteProperty(globalThis, 'window')
  Reflect.deleteProperty(globalThis, 'localStorage')
}

describe('Tokeness theme defaults', () => {
  afterEach(() => {
    removeStorageGlobals()
    document.body.removeAttribute('data-theme-preset')
    document.body.removeAttribute('data-theme-radius')
  })

  after(() => {
    domWindow.close()
  })

  test('applies defaults before React mounts while preserving a stored preference', () => {
    installStorageGlobals()

    const defaults = initializeThemeCustomizationDom()
    assert.equal(defaults.preset, 'sunset-glow')
    assert.equal(defaults.radius, 'none')
    assert.equal(document.body.dataset.themePreset, 'sunset-glow')
    assert.equal(document.body.dataset.themeRadius, 'none')

    localStorage.setItem('newapi:theme:v1:preset', 'ocean-breeze')
    localStorage.setItem('newapi:theme:v1:radius', 'xl')
    const stored = initializeThemeCustomizationDom()
    assert.equal(stored.preset, 'ocean-breeze')
    assert.equal(stored.radius, 'xl')
    assert.equal(document.body.dataset.themePreset, 'ocean-breeze')
    assert.equal(document.body.dataset.themeRadius, 'xl')
  })
})
