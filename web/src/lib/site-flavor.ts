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
 * Read the build-time site flavor baked by rsbuild `source.define`
 * (`VITE_SITE_FLAVOR` = 'mainland' | 'overseas' | unset).
 *
 * The exact `import.meta.env.VITE_SITE_FLAVOR` expression is textually
 * replaced at build time, but plain Node (node:test) has no
 * `import.meta.env`, so the read must be guarded for tests.
 */
export function getSiteFlavor(): string | undefined {
  try {
    return import.meta.env.VITE_SITE_FLAVOR
  } catch {
    return undefined
  }
}

/** True when the bundle was built for the mainland edition (tokeness.cn). */
export const IS_MAINLAND_SITE = getSiteFlavor() === 'mainland'
