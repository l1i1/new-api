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
/**
 * Sticky invite-code capture.
 *
 * A visitor can land anywhere on the site with a partner's code in the URL,
 * browse a few pages, and only then register. The code therefore has to survive
 * the whole visit: it is read off the URL, persisted, and kept in the URL while
 * the visitor navigates, so the address bar a user copies or bookmarks still
 * carries it. It stops mattering once the account exists — registration sends
 * the stored code once and clears it.
 */
import { getAffiliateCode, saveAffiliateCode } from './storage'

/**
 * Partner-console codes. `aff` stays supported as the alias the site's own
 * referral links use (and which existing partner links were shared with), so
 * both names resolve to the same attribution.
 */
export const PARTNER_CODE_PARAM = 'paff'
export const AFF_CODE_PARAM = 'aff'

/**
 * Read the invite code out of a query string. The partner parameter wins when
 * both are present: it is the one a partner link carries.
 */
export function readAffiliateCode(search: string): string {
  const params = new URLSearchParams(search)
  return (
    params.get(PARTNER_CODE_PARAM)?.trim() ||
    params.get(AFF_CODE_PARAM)?.trim() ||
    ''
  )
}

/**
 * Persist whatever the URL carries and return the code now in effect. An
 * unknown or malformed value never clears a code already captured — a visitor
 * following an unrelated link must not lose the attribution.
 */
export function captureAffiliateCode(search: string): string {
  const code = readAffiliateCode(search)
  if (code) {
    if (code !== getAffiliateCode()) {
      saveAffiliateCode(code)
    }
    return code
  }
  return getAffiliateCode()
}

/**
 * The URL to rewrite so the address bar keeps carrying the code, or an empty
 * string when there is nothing to do: no code captured, or the URL already
 * carries one (either spelling, whatever value it holds).
 */
export function urlWithAffiliateCode(href: string, code: string): string {
  if (!code) return ''
  let url: URL
  try {
    url = new URL(href)
  } catch {
    return ''
  }
  if (
    url.searchParams.has(PARTNER_CODE_PARAM) ||
    url.searchParams.has(AFF_CODE_PARAM)
  ) {
    return ''
  }
  url.searchParams.set(PARTNER_CODE_PARAM, code)
  return url.toString()
}
