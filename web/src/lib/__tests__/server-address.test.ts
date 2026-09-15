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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { resolveServerAddress } from '@/lib/server-address'
import { STATUS_STORAGE_KEY } from '@/lib/status-query'

/**
 * The address links must carry is the one clients can reach, so the panel
 * origin has to differ from the backend's unconfigured `http://localhost:3000`
 * default for these cases to mean anything. jsdom pins the origin to exactly
 * that value, so the browser boundary is stubbed instead.
 */
const PUBLIC_PANEL_ORIGIN = 'https://aicom360.com'

function servePanelFrom(origin: string): void {
  vi.stubGlobal('window', {
    location: { origin },
    localStorage: window.localStorage,
  })
}

function cacheServerAddress(value: string): void {
  window.localStorage.setItem(
    STATUS_STORAGE_KEY,
    JSON.stringify({ server_address: value })
  )
}

beforeEach(() => {
  window.localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

describe('resolveServerAddress', () => {
  test('keeps a configured address that clients can reach', () => {
    servePanelFrom(PUBLIC_PANEL_ORIGIN)
    cacheServerAddress('https://api.aicom360.com')

    expect(resolveServerAddress()).toBe('https://api.aicom360.com')
  })

  test('falls back to the panel origin when the configured address is the localhost default', () => {
    servePanelFrom(PUBLIC_PANEL_ORIGIN)
    cacheServerAddress('http://localhost:3000')

    expect(resolveServerAddress()).toBe(PUBLIC_PANEL_ORIGIN)
  })

  test('falls back to the panel origin when the configured address is loopback over IP', () => {
    servePanelFrom(PUBLIC_PANEL_ORIGIN)
    cacheServerAddress('http://127.0.0.1:3000')

    expect(resolveServerAddress()).toBe(PUBLIC_PANEL_ORIGIN)
  })

  test('falls back to the panel origin when the configured address is a localhost subdomain', () => {
    servePanelFrom(PUBLIC_PANEL_ORIGIN)
    cacheServerAddress('http://api.localhost:3000')

    expect(resolveServerAddress()).toBe(PUBLIC_PANEL_ORIGIN)
  })

  test('falls back to the panel origin when no status has been cached', () => {
    servePanelFrom(PUBLIC_PANEL_ORIGIN)

    expect(resolveServerAddress()).toBe(PUBLIC_PANEL_ORIGIN)
  })

  test('trims trailing slashes from the configured address', () => {
    servePanelFrom(PUBLIC_PANEL_ORIGIN)
    cacheServerAddress('https://api.aicom360.com//')

    expect(resolveServerAddress()).toBe('https://api.aicom360.com')
  })

  test('prefers the status passed by the caller over the cached snapshot', () => {
    servePanelFrom(PUBLIC_PANEL_ORIGIN)
    cacheServerAddress('http://localhost:3000')

    expect(
      resolveServerAddress({ server_address: 'https://api.aicom360.com' })
    ).toBe('https://api.aicom360.com')
  })
})
