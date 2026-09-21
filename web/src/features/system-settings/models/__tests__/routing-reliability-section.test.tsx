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
  ForceRetryStatusCodes: '400',
  NeverRetryStatusCodes: '504,524',
  MultiKeyCredentialRetryStatusCodes: '402,403,429',
  AutomaticRetryKeywords: '',
  NeverRetryKeywords: 'context length\nprompt is too long',
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

describe('never-retry error keywords', () => {
  it('groups the retry fields by what they do, in decision order', () => {
    const { container } = renderSection()

    const subheadings = Array.from(container.querySelectorAll('h5')).map(
      (node) => node.textContent
    )
    expect(subheadings).toEqual([
      'When to retry another channel',
      'When never to retry (wins over the rules above)',
      'Multi-key channels',
    ])

    // The two halves of the rule sit in different groups: the lists that cause a
    // failover come before the ones that stop it.
    const order = Array.from(container.querySelectorAll('label, h5')).map(
      (node) => node.textContent
    )
    const at = (label: string) => order.indexOf(label)
    const neverHeading = 'When never to retry (wins over the rules above)'
    expect(at('Auto-retry status codes')).toBeLessThan(at(neverHeading))
    expect(at('Force-retry status codes')).toBeLessThan(at(neverHeading))
    expect(at('Auto-retry error keywords')).toBeLessThan(at(neverHeading))
    expect(at(neverHeading)).toBeLessThan(at('Never-retry status codes'))
    expect(at('Never-retry error keywords')).toBeLessThan(at('Multi-key channels'))
    expect(at('Multi-key channels')).toBeLessThan(
      at('Multi-key retry status codes')
    )
  })

  it('edits the stored keyword list next to the retry keywords it mirrors', () => {
    renderSection()

    const field = screen.getByLabelText('Never-retry error keywords')
    expect(field).toHaveValue('context length\nprompt is too long')

    // The sibling list keeps its own field, so the two rules stay independent.
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
