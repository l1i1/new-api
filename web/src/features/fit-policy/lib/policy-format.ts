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

/** Label for the origin of a stored mark. */
export function fitCapabilitySourceLabelKey(source: string): string {
  return source === 'manual' ? 'Operator override' : 'Measured by a suite'
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
  if (input.document === input.baselineDocument) {
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

export type PolicySaveImpact = {
  /**
   * True when saving would leave the layer pinning nothing — an empty
   * document, a disabled one, or one whose families carry no rule. These are
   * legal states and one of them is the documented rollback, so they are
   * confirmed rather than blocked.
   */
  disablesLayer: boolean
}

/** Classify what saving a validated document would do to the live layer. */
export function policySaveImpact(
  validation: FitPolicyValidation | null
): PolicySaveImpact {
  if (validation === null || !validation.valid) {
    return { disablesLayer: false }
  }
  const codes = new Set(validation.warnings.map((warning) => warning.code))
  const disablesLayer =
    validation.summary === null ||
    codes.has('disabled') ||
    codes.has('cleared') ||
    codes.has('no_family') ||
    codes.has('no_rules')
  return { disablesLayer }
}
