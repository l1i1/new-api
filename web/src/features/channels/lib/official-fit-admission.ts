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

/**
 * Prefix-level view of the live policy's admission mode, as served by
 * `GET /api/fit-policy`'s `admission` field. The prefixes come from the Go
 * family table, so this module never needs its own copy of the family
 * classification.
 */
export type OfficialFitAdmissionView = {
  official_model_prefixes: string[]
  measured_model_prefixes: string[]
}

/** The state the server answers when no policy is installed. */
export const EMPTY_OFFICIAL_FIT_ADMISSION: OfficialFitAdmissionView = {
  official_model_prefixes: [],
  measured_model_prefixes: [],
}

function normalize(value: string) {
  return value.trim().toLowerCase()
}

/**
 * Whether one model id belongs to a family that runs measured admission: the
 * allowlist entry for it is ignored by the selector, and the server rejects
 * writing a new one. Case-insensitive prefix match, mirroring the Go table.
 */
export function isIgnoredOfficialFitModel(
  model: string,
  measuredPrefixes: string[]
): boolean {
  const normalized = normalize(model)
  if (!normalized) return false
  return measuredPrefixes.some((prefix) =>
    normalized.startsWith(normalize(prefix))
  )
}

/**
 * The allowlist field with entries a measured family ignores removed. The
 * input is the form's comma-separated text; entries are trimmed and empty
 * segments dropped, so a value that was only ignored entries becomes ''.
 */
export function filterIgnoredOfficialFitModels(
  value: string,
  measuredPrefixes: string[]
): string {
  if (!measuredPrefixes.length || !value) return value
  const kept = value
    .split(',')
    .map((entry) => entry.trim())
    .filter((entry) => entry !== '' && !isIgnoredOfficialFitModel(entry, measuredPrefixes))
  return kept.join(', ')
}

/**
 * Whether every official-fit family runs measured admission: the allowlist
 * has no reader left, so the channel form hides the input entirely.
 */
export function allOfficialFamiliesMeasured(
  admission: OfficialFitAdmissionView
): boolean {
  const official = admission.official_model_prefixes.map(normalize)
  if (!official.length) return false
  const measured = new Set(admission.measured_model_prefixes.map(normalize))
  return official.every((prefix) => measured.has(prefix))
}

/**
 * The prefixes to name in the field's note: families running measured
 * admission while at least one official family is still declared.
 */
export function measuredFamilyPrefixes(
  admission: OfficialFitAdmissionView
): string[] {
  if (allOfficialFamiliesMeasured(admission)) return []
  return admission.measured_model_prefixes.slice()
}
