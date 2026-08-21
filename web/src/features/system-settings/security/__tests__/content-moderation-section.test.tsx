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
import { describe, expect, test, vi } from 'vitest'

import {
  getContentModerationConfig,
  getContentModerationLogs,
  resetContentModerationUserViolations,
} from '../content-moderation-api'
import { ContentModerationSection } from '../content-moderation-section'

vi.mock('../content-moderation-api', () => ({
  getContentModerationConfig: vi.fn(),
  getContentModerationLogs: vi.fn(),
  resetContentModerationUserViolations: vi.fn(),
  updateContentModerationConfig: vi.fn(),
}))

const config = {
  enabled: true,
  mode: 'observe' as const,
  base_url: 'https://moderation.example.test',
  model: 'omni-moderation-latest',
  api_key_count: 1,
  api_key_suffixes: ['...1234'],
  thresholds: { sexual: 0.65 },
  all_groups: true,
  group_ids: [],
  all_models: true,
  models: [],
  model_filters: [],
  sample_rate: 1,
  timeout_ms: 1500,
  retry_count: 1,
  max_in_flight_per_key: 1,
  queue_wait_ms: 200,
  overload_status: 503 as const,
  key_cooldown_ms: 5000,
  record_non_hits: false,
  block_status: 403,
  block_message: 'Request blocked by content policy',
  email_on_hit: false,
  auto_ban_enabled: false,
  ban_threshold: 10,
  violation_window_hours: 24,
}

const log = {
  id: 1,
  user_id: 42,
  group: 'default',
  model: 'gpt-test',
  protocol: 'openai_chat',
  request_path: '/v1/chat/completions',
  request_id: 'req-moderation-123',
  mode: 'observe',
  action: 'observe',
  flagged: true,
  blocked: false,
  category: 'sexual',
  score: 0.9,
  excerpt: 'redacted',
  excerpt_hash: 'hash',
  latency_ms: 120,
  error: '',
  email_sent: false,
  created_at: 1760000000,
}

function renderSection() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <ContentModerationSection defaultValues={{}} />
    </QueryClientProvider>
  )
}

describe('content moderation logs', () => {
  test('shows request IDs, paginates, and resets a user violation count', async () => {
    const user = userEvent.setup()
    vi.mocked(getContentModerationConfig).mockResolvedValue({
      success: true,
      data: config,
    })
    vi.mocked(getContentModerationLogs).mockResolvedValue({
      success: true,
      data: [log],
      total: 21,
      offset: 0,
      limit: 20,
      page: 1,
      page_size: 20,
    })
    vi.mocked(resetContentModerationUserViolations).mockResolvedValue({
      success: true,
    })

    renderSection()

    expect(await screen.findByText('req-moderation-123')).toBeInTheDocument()
    expect(getContentModerationLogs).toHaveBeenCalledWith({
      offset: 0,
      limit: 20,
    })
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    await waitFor(() =>
      expect(getContentModerationLogs).toHaveBeenCalledWith({
        offset: 20,
        limit: 20,
      })
    )

    const pageSizeSelect = screen.getAllByRole('combobox').at(-1)
    if (!pageSizeSelect) {
      throw new Error('page size select was not rendered')
    }
    await user.click(pageSizeSelect)
    await user.click(await screen.findByRole('option', { name: '50 / page' }))
    await waitFor(() =>
      expect(getContentModerationLogs).toHaveBeenCalledWith({
        offset: 0,
        limit: 50,
      })
    )

    await user.click(screen.getByRole('button', { name: 'Reset count' }))
    await user.click(await screen.findByRole('button', { name: 'Reset count' }))
    await waitFor(() => {
      expect(
        vi.mocked(resetContentModerationUserViolations).mock.calls[0]?.[0]
      ).toBe(42)
    })
  })
})
