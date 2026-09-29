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
import { describe, expect, it } from 'vitest'

import {
  FIT_CAPABILITY_STATE_LABEL_KEYS,
  FIT_POLICY_WARNING_LEVELS,
  evaluatePolicySave,
  fitCapabilitySourceLabelKey,
  policySaveImpact,
  truncateFitHash,
} from '../lib/policy-format'
import type { FitPolicyValidation } from '../types'

function validation(
  overrides: Partial<FitPolicyValidation> = {}
): FitPolicyValidation {
  return {
    valid: true,
    error: '',
    divergence: '',
    warnings: [],
    summary: {
      version: 1,
      enabled: true,
      shadow: false,
      baseline: '',
      families: ['kimi-k3'],
      rules: 2,
      behaviors: 1,
      hash: 'abc',
    },
    live_hash: 'abc',
    ...overrides,
  }
}

describe('evaluatePolicySave', () => {
  const base = {
    document: '{"version":1}',
    baselineDocument: '{"version":0}',
    validatedDocument: '{"version":1}',
    validation: validation(),
    isRoot: true,
  }

  it('allows a validated, changed document for a super administrator', () => {
    expect(evaluatePolicySave(base)).toEqual({
      allowed: true,
      reasonKey: null,
    })
  })

  // The whole point of the gate: an uncompilable document must never be one
  // click away from replacing a working one.
  it('refuses a document that was never validated', () => {
    const gate = evaluatePolicySave({
      ...base,
      validatedDocument: null,
      validation: null,
    })
    expect(gate.allowed).toBe(false)
    expect(gate.reasonKey).toBe('Validate the document before saving it.')
  })

  it('refuses a document edited after the successful validation', () => {
    const gate = evaluatePolicySave({
      ...base,
      document: '{"version":2}',
    })
    expect(gate.allowed).toBe(false)
    expect(gate.reasonKey).toBe('Validate the document before saving it.')
  })

  it('refuses a document the backend rejected', () => {
    const gate = evaluatePolicySave({
      ...base,
      validation: validation({ valid: false, error: 'boom', summary: null }),
    })
    expect(gate.allowed).toBe(false)
    expect(gate.reasonKey).toBe(
      'The document does not compile, so it cannot be saved.'
    )
  })

  it('refuses a no-op save', () => {
    const gate = evaluatePolicySave({
      ...base,
      document: '{"version":0}',
      baselineDocument: '{"version":0}',
    })
    expect(gate.allowed).toBe(false)
    expect(gate.reasonKey).toBe('The document is unchanged.')
  })

  it('refuses everyone below the super administrator role', () => {
    const gate = evaluatePolicySave({ ...base, isRoot: false })
    expect(gate.allowed).toBe(false)
    expect(gate.reasonKey).toContain('super administrator')
  })
})

describe('policySaveImpact', () => {
  it('treats a blank document as switching the layer off', () => {
    expect(
      policySaveImpact(validation({ summary: null, live_hash: 'abc' }))
    ).toEqual({ disablesLayer: true })
  })

  it.each(['disabled', 'cleared', 'no_family', 'no_rules'] as const)(
    'treats %s as switching the layer off',
    (code) => {
      expect(
        policySaveImpact(validation({ warnings: [{ code }] })).disablesLayer
      ).toBe(true)
    }
  )

  it('does not confirm a shadow or baseline notice', () => {
    expect(
      policySaveImpact(
        validation({
          warnings: [{ code: 'shadow' }, { code: 'baseline_unset' }],
        })
      ).disablesLayer
    ).toBe(false)
  })

  it('does not confirm a document that failed validation', () => {
    expect(
      policySaveImpact(validation({ valid: false, summary: null }))
        .disablesLayer
    ).toBe(false)
  })
})

describe('fit policy presentation helpers', () => {
  it('truncates long hashes and leaves short ones alone', () => {
    expect(truncateFitHash('0123456789abcdef', 8)).toBe('01234567…')
    expect(truncateFitHash('abc')).toBe('abc')
    expect(truncateFitHash('')).toBe('')
    expect(truncateFitHash('   ')).toBe('')
  })

  it('labels the origin of a mark', () => {
    expect(fitCapabilitySourceLabelKey('manual')).toBe('Operator override')
    expect(fitCapabilitySourceLabelKey('suite')).toBe('Measured by a suite')
  })

  it('treats every way of doing nothing as critical', () => {
    expect(FIT_POLICY_WARNING_LEVELS.disabled).toBe('critical')
    expect(FIT_POLICY_WARNING_LEVELS.cleared).toBe('critical')
    expect(FIT_POLICY_WARNING_LEVELS.no_family).toBe('critical')
    expect(FIT_POLICY_WARNING_LEVELS.no_rules).toBe('critical')
    expect(FIT_POLICY_WARNING_LEVELS.shadow).toBe('warning')
    expect(FIT_POLICY_WARNING_LEVELS.baseline_unset).toBe('info')
  })

  it('has a label for every capability state the backend can send', () => {
    expect(Object.keys(FIT_CAPABILITY_STATE_LABEL_KEYS).sort()).toEqual([
      'manual_active',
      'manual_expired',
      'suite_failed',
      'suite_fresh',
      'suite_stale',
      'unknown',
    ])
  })
})
