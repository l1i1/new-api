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
import * as z from 'zod'

import type {
  ContentModerationConfig,
  UpdateContentModerationConfig,
} from './content-moderation-api'

export const contentModerationSchema = z.object({
  enabled: z.boolean(),
  mode: z.enum(['observe', 'pre_block']),
  base_url: z.string().url(),
  model: z.string().min(1),
  api_key: z.string(),
  clear_api_keys: z.boolean(),
  thresholds: z.string().refine((value) => {
    try {
      const parsed: unknown = JSON.parse(value)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        return false
      }
      return Object.values(parsed).every(
        (threshold) =>
          typeof threshold === 'number' &&
          Number.isFinite(threshold) &&
          threshold >= 0 &&
          threshold <= 1
      )
    } catch {
      return false
    }
  }),
  all_groups: z.boolean(),
  group_ids: z.string(),
  all_models: z.boolean(),
  models: z.string(),
  model_filters: z.string(),
  sample_rate: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isFinite(parsed) && parsed >= 0.01 && parsed <= 1
  }),
  timeout_ms: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isInteger(parsed) && parsed >= 1 && parsed <= 120000
  }),
  retry_count: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isInteger(parsed) && parsed >= 1 && parsed <= 5
  }),
  max_in_flight_per_key: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isInteger(parsed) && parsed >= 1 && parsed <= 64
  }),
  queue_wait_ms: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isInteger(parsed) && parsed >= 1 && parsed <= 10000
  }),
  overload_status: z.string().refine((value) => {
    const parsed = Number(value)
    return parsed === 429 || parsed === 503
  }),
  key_cooldown_ms: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isInteger(parsed) && parsed >= 100 && parsed <= 300000
  }),
  record_non_hits: z.boolean(),
  block_status: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isInteger(parsed) && parsed >= 400 && parsed <= 599
  }),
  block_message: z.string().min(1),
  email_on_hit: z.boolean(),
  auto_ban_enabled: z.boolean(),
  ban_threshold: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isInteger(parsed) && parsed >= 1
  }),
  violation_window_hours: z.string().refine((value) => {
    const parsed = Number(value)
    return Number.isInteger(parsed) && parsed >= 1 && parsed <= 8760
  }),
})

export type ContentModerationFormValues = z.input<
  typeof contentModerationSchema
>

const splitLines = (value: string) =>
  value
    .split('\n')
    .map((item) => item.trim())
    .filter(Boolean)

const formatThresholds = (thresholds: Record<string, number>) =>
  JSON.stringify(thresholds, null, 2)

export function toContentModerationFormValues(
  config: ContentModerationConfig
): ContentModerationFormValues {
  return {
    enabled: config.enabled,
    mode: config.mode,
    base_url: config.base_url,
    model: config.model,
    api_key: '',
    clear_api_keys: false,
    thresholds: formatThresholds(config.thresholds),
    all_groups: config.all_groups,
    group_ids: (config.group_ids ?? []).join('\n'),
    all_models: config.all_models,
    models: (config.models ?? []).join('\n'),
    model_filters: (config.model_filters ?? []).join('\n'),
    sample_rate: String(config.sample_rate),
    timeout_ms: String(config.timeout_ms),
    retry_count: String(config.retry_count),
    max_in_flight_per_key: String(config.max_in_flight_per_key),
    queue_wait_ms: String(config.queue_wait_ms),
    overload_status: String(config.overload_status),
    key_cooldown_ms: String(config.key_cooldown_ms),
    record_non_hits: config.record_non_hits,
    block_status: String(config.block_status),
    block_message: config.block_message,
    email_on_hit: config.email_on_hit,
    auto_ban_enabled: config.auto_ban_enabled,
    ban_threshold: String(config.ban_threshold),
    violation_window_hours: String(config.violation_window_hours),
  }
}

export function toContentModerationRequest(
  values: ContentModerationFormValues
): UpdateContentModerationConfig {
  const request: UpdateContentModerationConfig = {
    enabled: values.enabled,
    mode: values.mode,
    base_url: values.base_url.trim(),
    model: values.model.trim(),
    thresholds: JSON.parse(values.thresholds) as Record<string, number>,
    all_groups: values.all_groups,
    group_ids: splitLines(values.group_ids),
    all_models: values.all_models,
    models: splitLines(values.models),
    model_filters: splitLines(values.model_filters),
    sample_rate: Number(values.sample_rate),
    timeout_ms: Number(values.timeout_ms),
    retry_count: Number(values.retry_count),
    max_in_flight_per_key: Number(values.max_in_flight_per_key),
    queue_wait_ms: Number(values.queue_wait_ms),
    overload_status: Number(values.overload_status) as 429 | 503,
    key_cooldown_ms: Number(values.key_cooldown_ms),
    record_non_hits: values.record_non_hits,
    block_status: Number(values.block_status),
    block_message: values.block_message.trim(),
    email_on_hit: values.email_on_hit,
    auto_ban_enabled: values.auto_ban_enabled,
    ban_threshold: Number(values.ban_threshold),
    violation_window_hours: Number(values.violation_window_hours),
  }
  if (values.api_key.trim() !== '') {
    request.api_key = values.api_key
  } else if (values.clear_api_keys) {
    request.clear_api_keys = true
  }
  return request
}
