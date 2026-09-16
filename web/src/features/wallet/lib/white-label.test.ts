import { describe, expect, test } from 'vitest'

import { getWhiteLabelState } from './white-label'

describe('getWhiteLabelState', () => {
  test('returns null without a marker', () => {
    expect(getWhiteLabelState(null)).toBeNull()
    expect(getWhiteLabelState(undefined)).toBeNull()
    expect(getWhiteLabelState({})).toBeNull()
  })

  test('accepts a live marker in both setting shapes', () => {
    const future = Date.now() / 1000 + 3600
    const marker = { hide_referral: true, partner_id: 'tommy', until: future }
    expect(getWhiteLabelState({ white_label: marker })?.partnerId).toBe('tommy')
    expect(getWhiteLabelState(JSON.stringify({ white_label: marker }))?.partnerId).toBe('tommy')
  })

  test('rejects expired markers and non-referral markers', () => {
    const past = Date.now() / 1000 - 3600
    expect(
      getWhiteLabelState({ white_label: { hide_referral: true, partner_id: 'tommy', until: past } }),
    ).toBeNull()
    expect(
      getWhiteLabelState({ white_label: { hide_referral: false, partner_id: 'tommy' } }),
    ).toBeNull()
    expect(getWhiteLabelState('{invalid')).toBeNull()
  })
})
