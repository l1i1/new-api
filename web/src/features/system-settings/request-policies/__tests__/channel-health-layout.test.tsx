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
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { ChannelHealthSection } from '../channel-health-section'
import { defaultRequestPolicySettings } from '../defaults'

let client: QueryClient

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
})

afterEach(() => {
  cleanup()
  client.clear()
})

function show() {
  return render(
    <QueryClientProvider client={client}>
      <ChannelHealthSection defaultValues={defaultRequestPolicySettings} />
    </QueryClientProvider>
  )
}

const retryDefaults = {
  ...defaultRequestPolicySettings,
  RetryTimes: 4,
  NeverRetryKeywords: 'context length\nprompt is too long',
}

function showRetrySettings() {
  return render(
    <QueryClientProvider client={client}>
      <ChannelHealthSection defaultValues={retryDefaults} />
    </QueryClientProvider>
  )
}

function formItemOf(control: HTMLElement) {
  return control.closest('[data-slot=form-item]')
}

describe('channel health layout', () => {
  it('nests only the test mode and interval under the scheduled channel tests switch', () => {
    show()
    const options = screen.getByRole('group', {
      name: 'Scheduled test options',
    })
    expect(within(options).getByRole('combobox')).toBeVisible()
    expect(
      within(options).getByRole('spinbutton', {
        name: 'Test interval (minutes)',
      })
    ).toBeVisible()
    expect(
      within(options).queryByRole('spinbutton', {
        name: 'Channel test concurrency',
      })
    ).not.toBeInTheDocument()
    expect(within(options).queryByRole('switch')).not.toBeInTheDocument()
  })

  it('marks every switch row as full width so it never shares a grid row with an input', () => {
    show()
    for (const name of [
      'Scheduled channel tests',
      'Re-enable on success',
      'Disable on failure',
    ]) {
      expect(formItemOf(screen.getByRole('switch', { name }))).toHaveAttribute(
        'data-settings-form-span',
        'full'
      )
    }
  })

  it('lays out the auto-disable switch, inputs and keyword list in one shared grid', () => {
    show()
    const grid = formItemOf(
      screen.getByRole('switch', { name: 'Disable on failure' })
    )?.parentElement
    expect(grid).toHaveAttribute('data-settings-form-span', 'full')
    for (const control of [
      screen.getByRole('spinbutton', {
        name: 'Health check timeout threshold (seconds)',
      }),
      screen.getByRole('textbox', { name: 'Auto-disable status codes' }),
      screen.getByRole('textbox', { name: 'Failure keywords' }),
    ]) {
      expect(formItemOf(control)?.parentElement).toBe(grid)
    }
  })

  it('groups retry fields by decision order', () => {
    const { container } = showRetrySettings()

    const subheadings = [...container.querySelectorAll('h5')].map(
      (node) => node.textContent
    )
    expect(subheadings).toEqual([
      'When never to retry (checked first)',
      'When to retry another key of the same channel',
      'When to retry another channel',
    ])

    const order = [...container.querySelectorAll('label, h5')].map(
      (node) => node.textContent
    )
    const at = (label: string) => order.indexOf(label)
    const neverHeading = 'When never to retry (checked first)'
    const keyRotationHeading = 'When to retry another key of the same channel'
    const failoverHeading = 'When to retry another channel'

    // A never-retry verdict outranks both remedies, and rotating the key of the
    // same channel is cheaper than failing over, so the groups follow that order.
    expect(at(neverHeading)).toBeLessThan(at('Never-retry error keywords'))
    expect(at('Never-retry error keywords')).toBeLessThan(
      at(keyRotationHeading)
    )
    expect(at(keyRotationHeading)).toBeLessThan(
      at('Multi-key retry error keywords')
    )
    expect(at('Multi-key retry error keywords')).toBeLessThan(
      at(failoverHeading)
    )
    expect(at(failoverHeading)).toBeLessThan(at('Auto-retry error keywords'))
    expect(at('Force-retry status codes')).toBe(-1)
  })

  it('reads and edits the backend-provided never-retry keyword list', async () => {
    showRetrySettings()

    const field = screen.getByLabelText('Never-retry error keywords')
    expect(field).toHaveValue('context length\nprompt is too long')
    expect(screen.getByLabelText('Auto-retry error keywords')).toHaveValue('')

    const user = userEvent.setup()
    await user.clear(field)
    await user.type(field, 'serving window')
    expect(field).toHaveValue('serving window')
  })
})
