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

export interface GroupAccessPolicy {
  group_name: string
  blocked_channel_ids: number[]
  blocked_models: string[]
  blocked_groups: string[]
  content_moderation_disabled: boolean
  created_at?: number
  updated_at?: number
}

export interface GroupAccessPolicyInput {
  blocked_channel_ids: number[]
  blocked_models: string[]
  blocked_groups: string[]
  content_moderation_disabled: boolean
}

export interface GroupAccessPolicyResponse {
  success: boolean
  message?: string
  data?: GroupAccessPolicy
}

export async function getGroupAccessPolicy(
  groupName: string
): Promise<GroupAccessPolicyResponse> {
  const res = await api.get(
    `/api/group-access-policies/${encodeURIComponent(groupName)}`
  )
  return res.data
}

export async function replaceGroupAccessPolicy(
  groupName: string,
  policy: GroupAccessPolicyInput
): Promise<GroupAccessPolicyResponse> {
  const res = await api.put(
    `/api/group-access-policies/${encodeURIComponent(groupName)}`,
    policy
  )
  return res.data
}
