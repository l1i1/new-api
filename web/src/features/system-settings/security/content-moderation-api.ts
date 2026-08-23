/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { api } from '@/lib/api'

export type ContentModerationConfig = {
  enabled: boolean
  mode: 'observe' | 'pre_block'
  base_url: string
  model: string
  api_key_count: number
  api_key_suffixes?: string[]
  thresholds: Record<string, number>
  all_groups: boolean
  group_ids?: string[]
  all_models: boolean
  models?: string[]
  model_filters?: string[]
  sample_rate: number
  timeout_ms: number
  retry_count: number
  max_in_flight_per_key: number
  queue_wait_ms: number
  overload_status: 429 | 503
  key_cooldown_ms: number
  record_non_hits: boolean
  block_status: number
  block_message: string
  email_on_hit: boolean
  auto_ban_enabled: boolean
  ban_threshold: number
  violation_window_hours: number
}

export type ContentModerationLog = {
  id: number
  user_id: number
  group: string
  model: string
  protocol: string
  request_path: string
  request_id: string
  mode: string
  action: string
  capacity_reason?: string
  flagged: boolean
  blocked: boolean
  category: string
  score: number
  excerpt: string
  excerpt_hash: string
  latency_ms: number
  error: string
  email_sent: boolean
  created_at: number
}

type ConfigResponse = {
  success: boolean
  message?: string
  data: ContentModerationConfig
}

export type LogsResponse = {
  success: boolean
  message?: string
  data: ContentModerationLog[]
  total: number
  offset: number
  limit: number
  page: number
  page_size: number
}

export type ContentModerationLogsQuery = {
  offset: number
  limit: number
}

export type UpdateContentModerationConfig = {
  enabled: boolean
  mode: 'observe' | 'pre_block'
  base_url: string
  model: string
  api_key?: string
  clear_api_keys?: boolean
  thresholds: Record<string, number>
  all_groups: boolean
  group_ids: string[]
  all_models: boolean
  models: string[]
  model_filters: string[]
  sample_rate: number
  timeout_ms: number
  retry_count: number
  max_in_flight_per_key: number
  queue_wait_ms: number
  overload_status: 429 | 503
  key_cooldown_ms: number
  record_non_hits: boolean
  block_status: number
  block_message: string
  email_on_hit: boolean
  auto_ban_enabled: boolean
  ban_threshold: number
  violation_window_hours: number
}

export async function getContentModerationConfig() {
  const response = await api.get<ConfigResponse>(
    '/api/content-moderation/config'
  )
  return response.data
}

export async function updateContentModerationConfig(
  config: UpdateContentModerationConfig
) {
  const response = await api.put<ConfigResponse>(
    '/api/content-moderation/config',
    config
  )
  return response.data
}

export async function getContentModerationLogs(
  params: ContentModerationLogsQuery
) {
  const response = await api.get<LogsResponse>('/api/content-moderation/logs', {
    params,
  })
  return response.data
}

export async function resetContentModerationUserViolations(userID: number) {
  const response = await api.post<{ success: boolean; message?: string }>(
    `/api/content-moderation/users/${userID}/reset-violations`
  )
  return response.data
}
