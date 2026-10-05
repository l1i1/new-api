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
export type ApiResponse<T = unknown> = {
  success: boolean
  message: string
  data: T
}

/** Which document the running process is answering requests from. */
export type FitPolicySource = 'default' | 'cleared' | 'document'

/**
 * Ways a compilable document can still leave the layer doing less than it
 * looks like it does. The backend sends codes, not sentences, so the UI can
 * translate them.
 */
export type FitPolicyWarningCode =
  | 'disabled'
  | 'shadow'
  | 'no_family'
  | 'no_rules'
  | 'baseline_unset'
  | 'cleared'

export type FitPolicyWarning = {
  code: FitPolicyWarningCode
  count?: number
}

type FitPolicyLiveView = {
  installed: boolean
  version: number
  enabled: boolean
  shadow: boolean
  baseline: string
  hash: string
  last_error: string
}

export type FitPolicyView = {
  option_present: boolean
  source: FitPolicySource
  /** The stored option value, exactly as written; empty when never written. */
  document: string
  /** The document actually in force, indented for display. */
  effective_document: string
  parse_error: string
  divergence: string
  warnings: FitPolicyWarning[]
  live: FitPolicyLiveView
  default_document: string
  /**
   * Prefix-level admission summary of the live snapshot: every official-fit
   * family's model prefixes and the prefixes of the families that admit
   * channels by measurement. Both empty without an installed snapshot (the
   * everywhere-declared state).
   */
  admission: {
    official_model_prefixes: string[]
    measured_model_prefixes: string[]
  }
}

type FitPolicySummary = {
  version: number
  enabled: boolean
  shadow: boolean
  baseline: string
  families: string[]
  rules: number
  behaviors: number
  hash: string
}

export type FitPolicyValidation = {
  valid: boolean
  error: string
  divergence: string
  warnings: FitPolicyWarning[]
  summary: FitPolicySummary | null
  live_hash: string
}

export type FitCapabilityState =
  | 'unknown'
  | 'suite_fresh'
  | 'suite_stale'
  | 'suite_failed'
  | 'manual_active'
  | 'manual_expired'

export type FitCapabilityMark = {
  id: number
  channel_id: number
  channel_name: string
  family: string
  model: string
  behavior: string
  supported: boolean
  source: string
  suite: string
  cases: string
  rounds: number
  at: number
  policy_version: number
  policy_hash: string
  baseline_hash: string
  report_id: string
  run_id: string
  force: boolean
  revision: number
  updated_at: number
  /** State machine verdict for the bindings currently installed. */
  state: FitCapabilityState
  /** False when the mark was measured against a superseded policy/baseline. */
  binding_current: boolean
}

export type FitCapabilityPage = {
  items: FitCapabilityMark[]
  total: number
  page: number
  page_size: number
  policy_hash: string
  baseline_hash: string
}

export type FitCapabilityQuery = {
  page: number
  pageSize: number
  channelId?: string
  family?: string
  model?: string
  behavior?: string
  source?: string
  /** Tri-state: undefined means both, 'true'/'false' select. */
  supported?: 'true' | 'false'
}
