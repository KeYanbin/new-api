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
import { readCachedStatus, type StatusData } from '@/lib/status-query'

/** Hosts that only resolve on the machine running the server. */
function isLoopbackHost(hostname: string): boolean {
  return (
    hostname === 'localhost' ||
    hostname.endsWith('.localhost') ||
    hostname === '[::1]' ||
    hostname.startsWith('127.')
  )
}

function isLoopbackAddress(value: string): boolean {
  try {
    return isLoopbackHost(new URL(value).hostname)
  } catch {
    return false
  }
}

function readConfiguredAddress(status: StatusData | null | undefined): string {
  const value = status?.server_address
  return typeof value === 'string' ? value.trim().replace(/\/+$/, '') : ''
}

/**
 * Base address for links and configs consumed outside the panel: CC Switch
 * imports, connection info, chat presets, and code samples.
 *
 * A configured `server_address` wins, because the administrator declared it
 * reachable by clients. The backend reports `http://localhost:3000` until
 * someone configures it, and that address resolves only on the server itself,
 * so trusting it ships a dead link to every client. A loopback value is
 * therefore ignored and the origin the panel is served from is used instead —
 * it is the one address the browser has already proven reachable.
 *
 * Pass the `status` you already subscribe to; callers without one read the
 * cached snapshot.
 */
export function resolveServerAddress(status?: StatusData | null): string {
  const configured = readConfiguredAddress(status ?? readCachedStatus())
  if (configured && !isLoopbackAddress(configured)) return configured
  return window.location.origin
}
