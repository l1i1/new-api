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

// Bun loads dependency modules before each node:test file evaluates its own
// browser globals. antd-style reads the unqualified `matchMedia` binding
// during module initialization, so provide the browser API at preload time.
Object.defineProperty(globalThis, 'matchMedia', {
  configurable: true,
  value: (query: string): MediaQueryList => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  }),
})

// Some node:test files install HTMLElement without installing customElements
// before lazily importing UI modules that register a web component.
Object.defineProperty(globalThis, 'customElements', {
  configurable: true,
  value: {
    define: () => undefined,
    get: () => undefined,
    whenDefined: () => Promise.resolve(undefined),
    upgrade: () => undefined,
  },
})
