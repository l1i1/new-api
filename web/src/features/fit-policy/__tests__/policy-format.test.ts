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
  FIT_CAPABILITY_SOURCE_LABEL_KEYS,
  FIT_CAPABILITY_STATE_LABEL_KEYS,
  FIT_CAPABILITY_STATE_VARIANTS,
  FIT_POLICY_WARNING_LEVELS,
  POLICY_SAVE_IMPACT_MESSAGES,
  evaluatePolicySave,
  fitCapabilityBindingVerdict,
  fitCapabilitySourceLabelKey,
  fitCapabilityStateLabelKey,
  fitCapabilityStateVariant,
  fitPolicyWarningLevel,
  fitPolicyWarningMessageKey,
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

  // A node that failed to load a document keeps its last working snapshot. When
  // the cause was transient and the stored bytes are already correct, writing
  // the identical value is the only way to make it load again — and that is the
  // one write the unchanged check exists to refuse, so it needs an explicit way
  // through that does not quietly weaken the check for everybody else.
  it('allows re-writing the stored bytes when a reinstall was asked for', () => {
    expect(
      evaluatePolicySave({
        ...base,
        document: base.baselineDocument,
        validatedDocument: base.baselineDocument,
        validation: validation(),
        reinstall: true,
      })
    ).toEqual({ allowed: true, reasonKey: null })
  })

  it('still refuses a reinstall that was never validated', () => {
    const gate = evaluatePolicySave({
      ...base,
      document: base.baselineDocument,
      validatedDocument: null,
      validation: null,
      reinstall: true,
    })
    expect(gate.allowed).toBe(false)
    expect(gate.reasonKey).toBe('Validate the document before saving it.')
  })

  it('still refuses a reinstall for a non-root user', () => {
    const gate = evaluatePolicySave({
      ...base,
      document: base.baselineDocument,
      isRoot: false,
      reinstall: true,
    })
    expect(gate.allowed).toBe(false)
    expect(gate.reasonKey).toContain('super administrator')
  })

  it('still refuses a reinstall of a document that does not compile', () => {
    const gate = evaluatePolicySave({
      ...base,
      document: base.baselineDocument,
      validatedDocument: base.baselineDocument,
      validation: validation({ valid: false, error: 'boom', summary: null }),
      reinstall: true,
    })
    expect(gate.allowed).toBe(false)
    expect(gate.reasonKey).toBe(
      'The document does not compile, so it cannot be saved.'
    )
  })
})

describe('policySaveImpact', () => {
  // The two ways of taking the layer out are not the same state, and the
  // confirmation has to say which one is about to happen: a disabled policy is
  // still installed, so the suite applier keeps working; a cleared option is
  // not installed at all, so every report starts answering 409.
  it('distinguishes clearing the option from disabling the policy', () => {
    expect(
      policySaveImpact(
        validation({ summary: null, warnings: [{ code: 'cleared' }] })
      )
    ).toBe('cleared')
    expect(
      policySaveImpact(validation({ warnings: [{ code: 'disabled' }] }))
    ).toBe('disabled')
  })

  it.each(['no_family', 'no_rules'] as const)(
    'treats %s as a document that pins nothing',
    (code) => {
      expect(policySaveImpact(validation({ warnings: [{ code }] }))).toBe(
        'unpinning'
      )
    }
  )

  it('prefers the more specific outcome when several apply', () => {
    expect(
      policySaveImpact(
        validation({ warnings: [{ code: 'disabled' }, { code: 'no_family' }] })
      )
    ).toBe('disabled')
  })

  it('confirms a document with no summary at all', () => {
    expect(policySaveImpact(validation({ summary: null }))).toBe('unpinning')
  })

  it('does not confirm a shadow or baseline notice', () => {
    expect(
      policySaveImpact(
        validation({
          warnings: [{ code: 'shadow' }, { code: 'baseline_unset' }],
        })
      )
    ).toBe('none')
  })

  it('does not confirm a document that failed validation', () => {
    expect(policySaveImpact(validation({ valid: false, summary: null }))).toBe(
      'none'
    )
  })
})

describe('POLICY_SAVE_IMPACT_MESSAGES', () => {
  it('has a confirmation for every impact that needs one', () => {
    expect(Object.keys(POLICY_SAVE_IMPACT_MESSAGES).sort()).toEqual([
      'cleared',
      'disabled',
      'unpinning',
    ])
  })

  it('says that clearing the option makes suite reports fail', () => {
    expect(POLICY_SAVE_IMPACT_MESSAGES.cleared.description).toContain('409')
  })

  it('does not claim a disabled policy stops accepting reports', () => {
    // The distinction the old single sentence erased: enabled=false keeps the
    // snapshot installed, so the applier keeps recording measurements into it.
    expect(POLICY_SAVE_IMPACT_MESSAGES.disabled.description).toContain(
      'still accepted'
    )
    expect(POLICY_SAVE_IMPACT_MESSAGES.disabled.description).not.toContain(
      '409'
    )
  })

  it('does not reuse one sentence for both ways of switching the layer off', () => {
    expect(POLICY_SAVE_IMPACT_MESSAGES.disabled.description).not.toBe(
      POLICY_SAVE_IMPACT_MESSAGES.cleared.description
    )
    expect(POLICY_SAVE_IMPACT_MESSAGES.disabled.title).not.toBe(
      POLICY_SAVE_IMPACT_MESSAGES.cleared.title
    )
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

// A code or a state the deployed backend knows and this bundle does not used to
// index a map with it, get `undefined`, and render `<Icon />` — React's "Element
// type is invalid", which takes the whole tab down. A value the UI cannot name
// is an ordinary event (the two sides are deployed independently), so it has to
// degrade to something readable instead.
describe('unknown backend values degrade instead of crashing', () => {
  it('gives an unknown warning an informational level', () => {
    expect(fitPolicyWarningLevel('warning_from_a_newer_backend')).toBe('info')
    expect(fitPolicyWarningLevel('')).toBe('info')
  })

  it('renders an unknown warning code as its own text', () => {
    expect(fitPolicyWarningMessageKey('warning_from_a_newer_backend')).toBe(
      'warning_from_a_newer_backend'
    )
  })

  it('gives an unknown capability state a neutral variant and its own text', () => {
    expect(fitCapabilityStateVariant('state_from_a_newer_backend')).toBe(
      'secondary'
    )
    expect(fitCapabilityStateLabelKey('state_from_a_newer_backend')).toBe(
      'state_from_a_newer_backend'
    )
  })

  it('renders an unknown mark source as its own text', () => {
    expect(fitCapabilitySourceLabelKey('source_from_a_newer_backend')).toBe(
      'source_from_a_newer_backend'
    )
  })

  it('still answers with the shipped values for the codes it knows', () => {
    for (const code of Object.keys(FIT_POLICY_WARNING_LEVELS)) {
      expect(fitPolicyWarningLevel(code)).not.toBe('')
      expect(fitPolicyWarningMessageKey(code)).not.toBe(code)
    }
    for (const state of Object.keys(FIT_CAPABILITY_STATE_VARIANTS)) {
      expect(fitCapabilityStateVariant(state)).not.toBe('')
      expect(fitCapabilityStateLabelKey(state)).not.toBe(state)
    }
    for (const source of Object.keys(FIT_CAPABILITY_SOURCE_LABEL_KEYS)) {
      expect(fitCapabilitySourceLabelKey(source)).not.toBe(source)
    }
  })
})

// The listing's binding badge follows the state machine, which applies the
// hash comparison to a suite measurement alone. Rendering it for every row put
// "Operator mark" and "Measured against a superseded policy" in the same row.
describe('fitCapabilityBindingVerdict', () => {
  it('reports a stale binding for a suite measurement', () => {
    expect(
      fitCapabilityBindingVerdict({
        source: 'suite',
        binding_current: false,
      })
    ).toBe('superseded')
    expect(
      fitCapabilityBindingVerdict({ source: 'suite', binding_current: true })
    ).toBe('current')
  })

  it('does not apply the binding rule to an operator mark', () => {
    expect(
      fitCapabilityBindingVerdict({
        source: 'manual',
        binding_current: false,
      })
    ).toBe('not_applicable')
    expect(
      fitCapabilityBindingVerdict({ source: 'manual', binding_current: true })
    ).toBe('not_applicable')
  })
})
