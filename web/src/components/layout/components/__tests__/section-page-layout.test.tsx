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
import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import { SectionPageLayout } from '../section-page-layout'

function renderHeader(options: { stackActionsOnMobile?: boolean } = {}) {
  render(
    <SectionPageLayout stackActionsOnMobile={options.stackActionsOnMobile}>
      <SectionPageLayout.Title>API Keys</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <button type='button'>API Addresses</button>
        <button type='button'>Create API Key</button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <p>key rows</p>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )

  return {
    actions: screen.getByRole('button', { name: 'Create API Key' })
      .parentElement as HTMLElement,
    title: screen.getByRole('heading', { name: 'API Keys' }),
  }
}

describe('section page header layout', () => {
  test('a wide action cluster shrinks and wraps instead of overflowing the clipped main pane', () => {
    const { actions } = renderHeader()

    // The header lives inside `Main` (`overflow-hidden`). A non-shrinkable
    // action row kept its full intrinsic width, so the rightmost control (the
    // "Create API Key" button on a phone) rendered past the viewport edge
    // where the overflow was clipped and could not be reached or scrolled to.
    expect(actions).toHaveClass('min-w-0', 'flex-wrap')
    expect(actions).not.toHaveClass('shrink-0')
  })

  test('the title keeps shrinking so the actions row can take the line it needs', () => {
    const { title } = renderHeader()

    expect(title.parentElement).toHaveClass('min-w-0', 'flex-1')
  })

  test('stackActionsOnMobile gives the title a full line below the sm breakpoint', () => {
    const { title } = renderHeader({ stackActionsOnMobile: true })

    expect(title.parentElement).toHaveClass('max-sm:basis-full')
  })
})
