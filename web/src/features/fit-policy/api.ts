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
import { api } from '@/lib/api'
import { createServerError } from '@/lib/server-error-message'

import type {
  ApiResponse,
  FitCapabilityPage,
  FitCapabilityQuery,
  FitPolicyValidation,
  FitPolicyView,
} from './types'

/** The option key the policy document lives in. Mirrors fitpolicy.OptionKey. */
const FIT_POLICY_OPTION_KEY = 'official_fit.policy'

function requireData<T>(response: ApiResponse<T>): T {
  if (!response.success) {
    throw createServerError(response)
  }
  return response.data
}

/** Read the administration view of the policy document. */
export async function getFitPolicy(): Promise<FitPolicyView> {
  const response = await api.get<ApiResponse<FitPolicyView>>('/api/fit-policy')
  return requireData(response.data)
}

/**
 * Compile a candidate document without storing it.
 *
 * This is the mandatory pre-flight for the editor: a document that has not been
 * accepted here cannot be saved, so an uncompilable rule set is rejected before
 * it can replace a working one.
 */
export async function validateFitPolicyDocument(
  document: string
): Promise<FitPolicyValidation> {
  const response = await api.post<ApiResponse<FitPolicyValidation>>(
    '/api/fit-policy/validate',
    { document }
  )
  return requireData(response.data)
}

/** List capability marks across channels, one page at a time. */
export async function listFitCapabilityMarks(
  query: FitCapabilityQuery
): Promise<FitCapabilityPage> {
  const params = new URLSearchParams({
    p: String(query.page),
    page_size: String(query.pageSize),
  })
  for (const [key, value] of Object.entries({
    channel_id: query.channelId,
    family: query.family,
    model: query.model,
    behavior: query.behavior,
    source: query.source,
    supported: query.supported,
  })) {
    if (value) params.set(key, value)
  }
  const response = await api.get<ApiResponse<FitCapabilityPage>>(
    `/api/fit-capability/all?${params.toString()}`
  )
  return requireData(response.data)
}

/**
 * Persist a policy document through the generic option endpoint.
 *
 * That endpoint is root-only and already compiles the document before the
 * database commit (model.validateFitPolicyOption), so there is no second,
 * weaker write path to keep in sync. An empty document is the documented
 * rollback: it clears the option and drops the layer back to no opinion.
 */
export async function saveFitPolicyDocument(document: string): Promise<void> {
  const response = await api.put<ApiResponse<unknown>>('/api/option/', {
    key: FIT_POLICY_OPTION_KEY,
    value: document,
  })
  if (!response.data.success) {
    throw createServerError(response.data)
  }
}
