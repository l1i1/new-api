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
import { readdirSync, readFileSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { FORK_BUNDLES_BY_FILE } from '@/i18n/fork-bundles'

import {
  FIT_CAPABILITY_STATE_LABEL_KEYS,
  FIT_POLICY_SOURCE_LABEL_KEYS,
  FIT_POLICY_WARNING_MESSAGE_KEYS,
  evaluatePolicySave,
} from '../lib/policy-format'

/**
 * Every string this feature renders must exist in all seven locale bundles.
 *
 * The overlay gate already proves the seven overlays carry an identical key
 * set; this test proves the page does not depend on a key that was never
 * added, which would render as a raw English sentence in every other locale.
 * The navigation keys live outside this directory and are listed explicitly.
 */
const HERE = path.dirname(fileURLToPath(import.meta.url))
const FEATURE_DIR = path.resolve(HERE, '..')

function featureSources(): string[] {
  const files: string[] = []
  const walk = (directory: string) => {
    for (const entry of readdirSync(directory)) {
      const full = path.join(directory, entry)
      if (statSync(full).isDirectory()) {
        if (entry === '__tests__') continue
        walk(full)
        continue
      }
      if (/\.tsx?$/.test(entry)) files.push(full)
    }
  }
  walk(FEATURE_DIR)
  return files
}

function literalKeys(): string[] {
  const keys = new Set<string>()
  for (const file of featureSources()) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(/\bt\(\s*'([^']+)'/g)) {
      keys.add(match[1])
    }
  }
  for (const map of [
    FIT_POLICY_SOURCE_LABEL_KEYS,
    FIT_POLICY_WARNING_MESSAGE_KEYS,
    FIT_CAPABILITY_STATE_LABEL_KEYS,
  ]) {
    for (const value of Object.values(map)) keys.add(value)
  }
  return [...keys].sort()
}

/**
 * Every reason the save gate can render is a key returned at runtime rather
 * than a literal at a call site, so it is collected by exercising the gate
 * instead of by reading the source.
 */
function gateReasonKeys(): string[] {
  const allowed = {
    document: '{"version":1}',
    baselineDocument: '{"version":0}',
    validatedDocument: '{"version":1}',
    validation: {
      valid: true,
      error: '',
      divergence: '',
      warnings: [],
      summary: null,
      live_hash: '',
    },
    isRoot: true,
  }
  const blocked = [
    { ...allowed, isRoot: false },
    { ...allowed, document: '{"version":0}' },
    { ...allowed, validatedDocument: null, validation: null },
    {
      ...allowed,
      validation: { ...allowed.validation, valid: false },
    },
  ]
  return blocked
    .map((input) => evaluatePolicySave(input).reasonKey)
    .filter((key): key is string => key !== null)
}

/** Keys rendered by the sidebar and the tab title, which live elsewhere. */
const NAVIGATION_KEYS = [
  'Fit Capability',
  'Inspect behaviour-level channel capability marks and the official-fit policy document.',
]

describe('fit-policy i18n coverage', () => {
  const keys = [...literalKeys(), ...gateReasonKeys(), ...NAVIGATION_KEYS]

  it('uses a meaningful number of translatable strings', () => {
    expect(keys.length).toBeGreaterThan(40)
  })

  it('collects every reason the save gate can block a save with', () => {
    expect(gateReasonKeys()).toHaveLength(4)
  })

  it.each(Object.keys(FORK_BUNDLES_BY_FILE))(
    'has every used key in the %s bundle',
    (locale) => {
      const bundle =
        FORK_BUNDLES_BY_FILE[locale as keyof typeof FORK_BUNDLES_BY_FILE]
      const missing = keys.filter(
        (key) => !Object.hasOwn(bundle.translation, key)
      )
      expect(missing).toEqual([])
    }
  )
})
