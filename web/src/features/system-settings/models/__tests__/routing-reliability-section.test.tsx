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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'

import { RoutingReliabilitySection } from '../routing-reliability-section'

const client = new QueryClient({
  defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
})

const storedDefaults = {
  RetryTimes: 4,
  ChannelDisableThreshold: '',
  AutomaticDisableChannelEnabled: false,
  AutomaticEnableChannelEnabled: false,
  AutomaticDisableKeywords: '',
  AutomaticDisableStatusCodes: '401',
  AutomaticRetryStatusCodes: '100-199,300-407,409-503,505-523,525-599',
  NeverRetryStatusCodes: '504,524',
  MultiKeyCredentialRetryStatusCodes: '402,403,429',
  AutomaticRetryKeywords: '',
  NeverRetryKeywords: 'context length\nprompt is too long',
  MultiKeyCredentialRetryKeywords:
    'insufficient credits\ninsufficient balance\nbalance insufficient',
  'monitor_setting.auto_test_channel_enabled': false,
  'monitor_setting.auto_test_channel_minutes': 10,
  'monitor_setting.channel_test_concurrency': 1,
  'monitor_setting.channel_test_mode': 'scheduled_all' as const,
}

function renderSection() {
  return render(
    <QueryClientProvider client={client}>
      <RoutingReliabilitySection defaultValues={storedDefaults} />
    </QueryClientProvider>
  )
}

afterEach(() => {
  cleanup()
})

describe('retry rules', () => {
  it('groups the retry fields in the order the decision is made', () => {
    const { container } = renderSection()

    const subheadings = Array.from(container.querySelectorAll('h5')).map(
      (node) => node.textContent
    )
    expect(subheadings).toEqual([
      'When never to retry (checked first)',
      'When to retry another key of the same channel',
      'When to retry another channel',
    ])

    // A decision that stops the request is read before the ones that act on it,
    // and rotating the key comes before dropping the channel.
    const order = Array.from(container.querySelectorAll('label, h5')).map(
      (node) => node.textContent
    )
    const at = (label: string) => order.indexOf(label)
    const neverHeading = 'When never to retry (checked first)'
    const keyHeading = 'When to retry another key of the same channel'
    const channelHeading = 'When to retry another channel'
    expect(at('Never-retry status codes')).toBeGreaterThan(at(neverHeading))
    expect(at('Never-retry error keywords')).toBeLessThan(at(keyHeading))
    expect(at('Multi-key retry status codes')).toBeLessThan(at(channelHeading))
    expect(at('Multi-key retry error keywords')).toBeLessThan(at(channelHeading))
    expect(at('Auto-retry status codes')).toBeLessThan(at('Auto-retry error keywords'))
  })

  it('edits the key-scoped keyword list and keeps it independent', () => {
    renderSection()

    const field = screen.getByLabelText('Multi-key retry error keywords')
    expect(field).toHaveValue(
      'insufficient credits\ninsufficient balance\nbalance insufficient'
    )

    // The channel-failover keyword list keeps its own field.
    expect(screen.getByLabelText('Auto-retry error keywords')).toHaveValue('')
  })

  it('accepts an operator edit, including clearing the list', async () => {
    const user = userEvent.setup()
    renderSection()

    const field = screen.getByLabelText('Never-retry error keywords')
    await user.clear(field)
    expect(field).toHaveValue('')

    await user.type(field, 'serving window')
    expect(field).toHaveValue('serving window')
  })
})
