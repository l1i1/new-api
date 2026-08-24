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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import {
  getChannel,
  getChannels,
  searchChannels,
} from '@/features/channels/api'
import { getGroups } from '@/features/users/api'

import {
  getGroupAccessPolicy,
  replaceGroupAccessPolicy,
} from '../group-access-policies-api'
import { GroupAccessPoliciesSection } from '../group-access-policies-section'

vi.mock('@/features/channels/api', () => ({
  getChannel: vi.fn(),
  getChannels: vi.fn(),
  searchChannels: vi.fn(),
}))

vi.mock('@/features/users/api', () => ({
  getGroups: vi.fn(),
}))

vi.mock('../group-access-policies-api', () => ({
  getGroupAccessPolicy: vi.fn(),
  replaceGroupAccessPolicy: vi.fn(),
}))

const loadedPolicy = {
  group_name: 'default',
  blocked_channel_ids: [12, 999],
  blocked_models: ['gpt-blocked'],
  blocked_groups: ['vip'],
  content_moderation_disabled: false,
}

function renderSection() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <GroupAccessPoliciesSection />
    </QueryClientProvider>
  )
}

describe('group access policies', () => {
  beforeEach(() => {
    vi.mocked(getGroups).mockResolvedValue({
      success: true,
      data: ['default', 'vip'],
    })
    vi.mocked(getGroupAccessPolicy).mockResolvedValue({
      success: true,
      data: loadedPolicy,
    })
    vi.mocked(getChannel).mockImplementation(async (channelId) => ({
      success: channelId === 12,
      data:
        channelId === 12
          ? ({ id: 12, name: 'Channel Twelve' } as never)
          : undefined,
    }))
    vi.mocked(getChannels).mockResolvedValue({
      success: true,
      data: { items: [], total: 0, page: 1, page_size: 20 },
    })
    vi.mocked(searchChannels).mockResolvedValue({
      success: true,
      data: { items: [], total: 0 },
    })
    vi.mocked(replaceGroupAccessPolicy).mockResolvedValue({
      success: true,
      data: loadedPolicy,
    })
  })

  test('keeps missing blocked channels visible for explicit cleanup', async () => {
    renderSection()

    expect(await screen.findByText('Channel Twelve (#12)')).toBeInTheDocument()
    expect(await screen.findByText('Stale')).toBeInTheDocument()
    expect(screen.getByText('#999')).toBeInTheDocument()
    expect(screen.getByDisplayValue('gpt-blocked')).toBeInTheDocument()
  })

  test('normalizes model lines and replaces the complete selected-group policy', async () => {
    const user = userEvent.setup()
    renderSection()

    const models = await screen.findByDisplayValue('gpt-blocked')
    await user.clear(models)
    await user.type(models, ' gpt-next \n\ngpt-next\nclaude-next ')
    await user.click(screen.getByRole('switch'))
    await user.click(
      screen.getByRole('button', { name: 'Save group access policy' })
    )

    await waitFor(() =>
      expect(replaceGroupAccessPolicy).toHaveBeenCalledWith('default', {
        blocked_channel_ids: [12, 999],
        blocked_models: ['gpt-next', 'claude-next'],
        blocked_groups: ['vip'],
        content_moderation_disabled: true,
      })
    )
  })

  test('restores the cached policy when switching back to a previously loaded group', async () => {
    const user = userEvent.setup()
    vi.mocked(getGroupAccessPolicy).mockImplementation(async (groupName) => ({
      success: true,
      data: {
        ...loadedPolicy,
        group_name: groupName,
        blocked_models: [`${groupName}-model`],
      },
    }))
    renderSection()

    const groupSelect = await screen.findByLabelText('Base user group')
    expect(await screen.findByDisplayValue('default-model')).toBeInTheDocument()
    await user.selectOptions(groupSelect, 'vip')
    expect(await screen.findByDisplayValue('vip-model')).toBeInTheDocument()
    await user.selectOptions(groupSelect, 'default')
    expect(await screen.findByDisplayValue('default-model')).toBeInTheDocument()
  })
})
