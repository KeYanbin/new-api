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
import { describe, expect, test } from 'vitest'

import type { BillingSettings } from '../../types'
import { getRatioProtectSources } from '../ratio-protect-sources'

// Only the protection keys are read by the migration.
function settings(values: Partial<BillingSettings>): BillingSettings {
  return values as BillingSettings
}

describe('ratio protect channel binding migration', () => {
  test('expands legacy shared settings into one binding per selected channel', () => {
    expect(
      getRatioProtectSources(
        settings({
          'ratio_protect_setting.channel_ids': [11, 12],
          'ratio_protect_setting.endpoint': '/api/pricing',
          'ratio_protect_setting.sync_groups': ['vip'],
          'ratio_protect_setting.markup_mode': 'multiply',
          'ratio_protect_setting.markup_value': 1.5,
        })
      )
    ).toEqual([
      {
        channel_id: 11,
        source: 'newapi',
        endpoint: '/api/pricing',
        upstream_group: '',
        sync_groups: ['vip'],
        markup_mode: 'multiply',
        markup_value: 1.5,
      },
      {
        channel_id: 12,
        source: 'newapi',
        endpoint: '/api/pricing',
        upstream_group: '',
        sync_groups: ['vip'],
        markup_mode: 'multiply',
        markup_value: 1.5,
      },
    ])
  })

  test('recovers the single legacy channel and the Sub2API plaza endpoint', () => {
    expect(
      getRatioProtectSources(
        settings({
          'ratio_protect_setting.channel_id': 7,
          'ratio_protect_setting.endpoint': '/api/v1/model-plaza',
          'ratio_protect_setting.markup_mode': 'add',
          'ratio_protect_setting.markup_value': 0.2,
        })
      )
    ).toEqual([
      {
        channel_id: 7,
        source: 'sub2api',
        endpoint: '/api/v1/model-plaza',
        upstream_group: '',
        sync_groups: [],
        markup_mode: 'add',
        markup_value: 0.2,
      },
    ])
  })

  test('keeps saved bindings and ignores the legacy keys they replace', () => {
    const saved = [
      {
        channel_id: 11,
        source: 'sub2api' as const,
        endpoint: '',
        upstream_group: 'trial',
        sync_groups: [],
        markup_mode: 'multiply' as const,
        markup_value: 1.1,
      },
    ]
    expect(
      getRatioProtectSources(
        settings({
          'ratio_protect_setting.sources': JSON.stringify(saved),
          'ratio_protect_setting.channel_ids': [11, 12],
        })
      )
    ).toEqual(saved)
  })

  test('an explicitly saved empty list overrides legacy channels', () => {
    expect(
      getRatioProtectSources(
        settings({
          'ratio_protect_setting.sources': '[]',
          'ratio_protect_setting.channel_ids': [11, 12],
          'ratio_protect_setting.channel_id': 11,
        })
      )
    ).toEqual([])
  })

  test('returns no binding when nothing is configured', () => {
    expect(getRatioProtectSources(settings({}))).toEqual([])
  })
})
