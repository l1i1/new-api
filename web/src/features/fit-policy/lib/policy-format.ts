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
import type {
  FitCapabilityState,
  FitPolicySource,
  FitPolicyValidation,
  FitPolicyWarningCode,
} from '../types'

/**
 * Presentation rules for the fit-policy administration page, kept pure so the
 * decisions that matter — when a save is allowed, and whether a save switches
 * the layer off — are unit-testable without rendering anything.
 */

/** Plain-text label keys for the document actually in force. */
export const FIT_POLICY_SOURCE_LABEL_KEYS: Record<FitPolicySource, string> = {
  default: 'Shipped default',
  cleared: 'Cleared by an administrator',
  document: 'Administrator document',
}

/**
 * Severity of each warning the backend can report. `critical` means the layer
 * does less than a reader would assume from the document alone, so saving such
 * a document is confirmed explicitly rather than applied quietly.
 */
export const FIT_POLICY_WARNING_LEVELS: Record<
  FitPolicyWarningCode,
  'critical' | 'warning' | 'info'
> = {
  disabled: 'critical',
  cleared: 'critical',
  no_family: 'critical',
  no_rules: 'critical',
  shadow: 'warning',
  baseline_unset: 'info',
}

/** Message keys are the English sentences themselves, as everywhere else. */
export const FIT_POLICY_WARNING_MESSAGE_KEYS: Record<
  FitPolicyWarningCode,
  string
> = {
  disabled: 'The policy is disabled: no request is pinned.',
  shadow:
    'The policy is in shadow mode: requirements are decided but never enforced.',
  no_family:
    'The document declares no family, so no request can require anything.',
  no_rules:
    'The document declares no rule, so no request can require anything.',
  baseline_unset:
    'No baseline is set, so measurements are not bound to an official baseline.',
  cleared: 'The document is empty: the layer is installed with no opinion.',
}

/** Human-readable label for each capability state. */
export const FIT_CAPABILITY_STATE_LABEL_KEYS: Record<
  FitCapabilityState,
  string
> = {
  unknown: 'Unknown',
  suite_fresh: 'Suite verified',
  suite_stale: 'Stale measurement',
  suite_failed: 'Suite failed',
  manual_active: 'Operator mark',
  manual_expired: 'Operator mark expired',
}

/** Badge variant for each capability state. */
export const FIT_CAPABILITY_STATE_VARIANTS: Record<
  FitCapabilityState,
  'default' | 'secondary' | 'destructive' | 'warning' | 'outline'
> = {
  unknown: 'secondary',
  suite_fresh: 'default',
  suite_stale: 'warning',
  suite_failed: 'destructive',
  manual_active: 'default',
  manual_expired: 'warning',
}

/**
 * Message keys for the origin of a mark, as a map for the same reason the other
 * three are: the i18n coverage test can then collect them by reading the module
 * instead of by guessing what the function returns.
 */
export const FIT_CAPABILITY_SOURCE_LABEL_KEYS = {
  manual: 'Operator override',
  suite: 'Measured by a suite',
} as const

/** Label for the origin of a stored mark; an unknown source is shown verbatim. */
export function fitCapabilitySourceLabelKey(source: string): string {
  return (
    FIT_CAPABILITY_SOURCE_LABEL_KEYS[
      source as keyof typeof FIT_CAPABILITY_SOURCE_LABEL_KEYS
    ] ?? source
  )
}

/**
 * The severity of a warning the backend sent.
 *
 * The backend may ship a code this bundle has never heard of (the two are
 * deployed independently), and the previous direct map lookup returned
 * `undefined`, which reached `WARNING_ICONS[undefined]` and rendered
 * `<Icon />` — React's "Element type is invalid", which blanks the whole tab.
 * An unknown warning is not a crash and not silence: it is informational, and
 * `fitPolicyWarningMessageKey` below shows the code itself.
 */
export function fitPolicyWarningLevel(
  code: string
): 'critical' | 'warning' | 'info' {
  return FIT_POLICY_WARNING_LEVELS[code as FitPolicyWarningCode] ?? 'info'
}

/** The message key for a warning; an unknown code is rendered verbatim. */
export function fitPolicyWarningMessageKey(code: string): string {
  return FIT_POLICY_WARNING_MESSAGE_KEYS[code as FitPolicyWarningCode] ?? code
}

/** The badge variant for a capability state; an unknown state is neutral. */
export function fitCapabilityStateVariant(
  state: string
): 'default' | 'secondary' | 'destructive' | 'warning' | 'outline' {
  return (
    FIT_CAPABILITY_STATE_VARIANTS[state as FitCapabilityState] ?? 'secondary'
  )
}

/** The label key for a capability state; an unknown state is shown verbatim. */
export function fitCapabilityStateLabelKey(state: string): string {
  return FIT_CAPABILITY_STATE_LABEL_KEYS[state as FitCapabilityState] ?? state
}

export type FitCapabilityBindingVerdict =
  | 'current'
  | 'superseded'
  | 'not_applicable'

/**
 * Whether a mark's provenance can be stale at all, and if so, whether it is.
 *
 * Only a suite measurement is invalidated by the policy/baseline hash — the
 * state machine compares hashes after `source == suite && supported && not
 * expired`, and an operator mark is a human decision that no hash invalidates.
 * Judging every row by hash made a stored operator mark carry "Operator mark"
 * and "Measured against a superseded policy" at the same time, which reads as a
 * contradiction and is not one the state machine recognises.
 */
export function fitCapabilityBindingVerdict(mark: {
  source: string
  binding_current: boolean
}): FitCapabilityBindingVerdict {
  if (mark.source !== 'suite') return 'not_applicable'
  return mark.binding_current ? 'current' : 'superseded'
}

/**
 * Truncate a provenance hash for display.
 *
 * The full value is never useful in a table — the reader compares two hashes
 * for equality, and a 64-character column makes every other column unreadable.
 */
export function truncateFitHash(hash: string, length = 12): string {
  const value = hash?.trim() ?? ''
  if (value === '') return ''
  return value.length > length ? `${value.slice(0, length)}…` : value
}

export type PolicySaveGate = {
  allowed: boolean
  /** i18n key explaining the block; null when the save is allowed. */
  reasonKey: string | null
}

export type PolicySaveInput = {
  /** The text currently in the editor. */
  document: string
  /** The document as loaded from the server, to detect a no-op save. */
  baselineDocument: string
  /** The text the last successful validation was performed on. */
  validatedDocument: string | null
  validation: FitPolicyValidation | null
  isRoot: boolean
  /**
   * A deliberate re-write of bytes that are already stored.
   *
   * It exists because a node can fail to load a document and keep its
   * last-known-good snapshot. When the cause was transient and the stored bytes
   * are already correct, writing the identical value is the only way to make the
   * fleet install it again — and that is exactly the write the unchanged check
   * refuses. It relaxes that check and nothing else: role, validation and the
   * impact confirmation all still apply.
   */
  reinstall?: boolean
}

/**
 * Decide whether the editor may save, and say why not.
 *
 * An uncompilable document must never reach the option API — not because the
 * backend would accept it (it compiles the document before the commit), but
 * because a silent rejection after a click is a worse experience than a button
 * that explains itself. The gate also refuses to save a document that has not
 * been validated *at its current text*, so editing after a successful check
 * re-arms the check.
 */
export function evaluatePolicySave(input: PolicySaveInput): PolicySaveGate {
  if (!input.isRoot) {
    return {
      allowed: false,
      reasonKey:
        'The policy document is stored in the root-only option API, so changing it requires the super administrator role.',
    }
  }
  if (!input.reinstall && input.document === input.baselineDocument) {
    return { allowed: false, reasonKey: 'The document is unchanged.' }
  }
  if (input.validation === null || input.validatedDocument !== input.document) {
    return {
      allowed: false,
      reasonKey: 'Validate the document before saving it.',
    }
  }
  if (!input.validation.valid) {
    return {
      allowed: false,
      reasonKey: 'The document does not compile, so it cannot be saved.',
    }
  }
  return { allowed: true, reasonKey: null }
}

/**
 * What saving a validated document would do to the layer.
 *
 * These are not interchangeable, which is why this is an enum and not the
 * boolean it used to be. The two ways of taking the layer out differ in what
 * still works afterwards:
 *
 *  - `disabled` — `enabled=false`. The document compiles and a snapshot is
 *    installed, so `POST /api/fit-capability/report` still accepts and records
 *    suite reports; nothing is pinned to a channel.
 *  - `cleared` — an empty document. No snapshot is installed at all, so every
 *    suite report is refused with 409 until a document is written back.
 *  - `unpinning` — a document that is installed but declares nothing a request
 *    can require: no family, or families with no rule.
 *
 * The old boolean answered "the layer is off" for all three, so the operator
 * confirming the save could not tell which of them they were about to cause.
 */
export type PolicySaveImpact = 'none' | 'disabled' | 'cleared' | 'unpinning'

/** Classify what saving a validated document would do to the live layer. */
export function policySaveImpact(
  validation: FitPolicyValidation | null
): PolicySaveImpact {
  if (validation === null || !validation.valid) {
    return 'none'
  }
  const codes = new Set(validation.warnings.map((warning) => warning.code))
  if (codes.has('cleared')) {
    return 'cleared'
  }
  if (codes.has('disabled')) {
    return 'disabled'
  }
  if (
    validation.summary === null ||
    codes.has('no_family') ||
    codes.has('no_rules')
  ) {
    return 'unpinning'
  }
  return 'none'
}

/**
 * Title and body of the confirmation a save needs, per impact.
 *
 * The cleared body has to name the consequence the operator cannot see from the
 * editor: an empty document is not "a disabled policy", it is no policy at all,
 * and the suite applier starts getting 409s the moment it lands.
 */
export const POLICY_SAVE_IMPACT_MESSAGES: Record<
  Exclude<PolicySaveImpact, 'none'>,
  { title: string; description: string }
> = {
  disabled: {
    title: 'Switch the official-fit policy layer off?',
    description:
      'Saving this document installs a disabled policy: the snapshot stays in place, so suite reports are still accepted and recorded, but no request is pinned to a channel that reproduces the official behaviour.',
  },
  cleared: {
    title: 'Clear the official-fit policy document?',
    description:
      'Saving the empty document clears the option, so no policy is installed at all: suite reports are refused with 409 until a document is written back, and no request is pinned meanwhile.',
  },
  unpinning: {
    title: 'Switch the official-fit policy layer off?',
    description:
      'Saving this document switches the official-fit policy layer off: no request is pinned to a channel that reproduces the official behaviour.',
  },
}
