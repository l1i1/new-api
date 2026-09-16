import type { AuthUser } from '@/stores/auth-store'

export interface WhiteLabelState {
  hideReferral: boolean
  partnerId: string
}

type UserSettingValue = AuthUser['setting']

/**
 * Reads the server-stamped white-label marker from the self payload.
 * The marker expires with the user's commission window: an expired or
 * absent marker restores the default interface.
 */
export function getWhiteLabelState(setting: UserSettingValue | null | undefined): WhiteLabelState | null {
  if (!setting) return null
  const parsed: Record<string, unknown> =
    typeof setting === 'string' ? parseSettingString(setting) : setting
  const whiteLabel = parsed.white_label as
    | { hide_referral?: unknown; partner_id?: unknown; until?: unknown }
    | undefined
  if (!whiteLabel || whiteLabel.hide_referral !== true) return null
  if (typeof whiteLabel.partner_id !== 'string' || whiteLabel.partner_id === '') return null
  if (typeof whiteLabel.until === 'number' && whiteLabel.until > 0 && Date.now() / 1000 > whiteLabel.until) {
    return null
  }
  return { hideReferral: true, partnerId: whiteLabel.partner_id }
}

function parseSettingString(raw: string): Record<string, unknown> {
  try {
    const parsed: unknown = JSON.parse(raw)
    if (parsed && typeof parsed === 'object') return parsed as Record<string, unknown>
  } catch {
    // Corrupt setting blobs restore the default interface.
  }
  return {}
}
