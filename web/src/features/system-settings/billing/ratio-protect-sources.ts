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
import type { BillingSettings, RatioProtectChannelSource } from '../types'

/**
 * Channel bindings of the protection settings, derived from the saved option
 * when the administrator has never edited them.
 *
 * Settings written before per-channel bindings existed kept one endpoint, one
 * group filter and one markup shared by every selected channel. They are folded
 * into one binding per channel so the form shows the effective configuration,
 * and saving writes the new shape.
 */
export function getRatioProtectSources(
  settings: BillingSettings
): RatioProtectChannelSource[] {
  let saved: unknown
  try {
    saved = JSON.parse(settings['ratio_protect_setting.sources'] || 'null')
  } catch {
    return []
  }
  if (Array.isArray(saved)) return saved as RatioProtectChannelSource[]
  let channelIds = settings['ratio_protect_setting.channel_ids'] ?? []
  if (channelIds.length === 0 && settings['ratio_protect_setting.channel_id']) {
    channelIds = [settings['ratio_protect_setting.channel_id']]
  }
  if (channelIds.length === 0) return []
  const endpoint = settings['ratio_protect_setting.endpoint'] ?? ''
  return channelIds.map((channelId) => ({
    channel_id: channelId,
    source: endpoint.includes('/model-plaza') ? 'sub2api' : 'newapi',
    endpoint,
    upstream_group: '',
    sync_groups: settings['ratio_protect_setting.sync_groups'] ?? [],
    markup_mode: settings['ratio_protect_setting.markup_mode'],
    markup_value: settings['ratio_protect_setting.markup_value'],
  }))
}
