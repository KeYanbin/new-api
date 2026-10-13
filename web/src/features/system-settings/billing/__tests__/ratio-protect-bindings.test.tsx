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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, test, vi } from 'vitest'

import type {
  RatioProtectChannelSource,
  UpstreamChannel,
  UpstreamPriceGroup,
  UpstreamPriceGroupsResponse,
} from '../../types'
import { RatioProtectBindings } from '../ratio-protect-bindings'

const { getUpstreamPriceGroups } = vi.hoisted(() => ({
  getUpstreamPriceGroups: vi.fn(),
}))

vi.mock('../../api', () => ({ getUpstreamPriceGroups }))

const channels: UpstreamChannel[] = [
  { id: 11, name: 'kedaya-a', base_url: 'https://a.example', status: 1 },
  { id: 12, name: 'kedaya-b', base_url: 'https://b.example', status: 1 },
]

const plazaGroups: UpstreamPriceGroup[] = [
  {
    id: 7,
    name: 'trial',
    platform: 'openai',
    rate_multiplier: 0.07,
    peak_rate_enabled: false,
    peak_rate_multiplier: 1,
    model_count: 163,
  },
  {
    id: 8,
    name: 'pro',
    platform: 'openai',
    rate_multiplier: 0.8,
    peak_rate_enabled: false,
    peak_rate_multiplier: 1,
    model_count: 163,
  },
]

const plazaResponse: UpstreamPriceGroupsResponse = {
  success: true,
  message: '',
  data: plazaGroups,
}

function binding(
  overrides: Partial<RatioProtectChannelSource> = {}
): RatioProtectChannelSource {
  return {
    channel_id: 11,
    source: 'newapi',
    endpoint: '',
    upstream_group: '',
    sync_groups: [],
    markup_mode: 'add',
    markup_value: 0,
    ...overrides,
  }
}

function Fixture(props: {
  initial: RatioProtectChannelSource[]
  onChange?: (value: RatioProtectChannelSource[]) => void
}) {
  const [value, setValue] = useState(props.initial)
  return (
    <RatioProtectBindings
      value={value}
      onChange={(next) => {
        setValue(next)
        props.onChange?.(next)
      }}
      channels={channels}
      groupOptions={[{ label: 'default', value: 'default' }]}
      defaultMarkupMode='add'
      defaultMarkupValue={0.1}
    />
  )
}

function renderBindings(
  initial: RatioProtectChannelSource[],
  onChange?: (value: RatioProtectChannelSource[]) => void
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <Fixture initial={initial} onChange={onChange} />
    </QueryClientProvider>
  )
}

describe('ratio protect bindings', () => {
  test('reads the price group of the upstream a Sub2API channel talks to', async () => {
    getUpstreamPriceGroups.mockResolvedValue(plazaResponse)
    renderBindings([
      binding({
        channel_id: 12,
        source: 'sub2api',
        upstream_group: 'trial',
        markup_mode: 'multiply',
        markup_value: 1.2,
      }),
    ])

    expect(screen.getByText('kedaya-b')).toBeInTheDocument()
    expect(screen.getByText('Local channel')).toBeInTheDocument()
    expect(screen.getByText('Upstream type')).toBeInTheDocument()
    expect(screen.getByText('trial')).toBeInTheDocument()

    await waitFor(() =>
      expect(getUpstreamPriceGroups).toHaveBeenCalledWith({
        channelId: 12,
        endpoint: undefined,
      })
    )
    expect(
      await screen.findByText(
        'Upstream charges 0.07x of the group base price for 163 models.'
      )
    ).toBeInTheDocument()
  })

  test('only asks for a price group when the binding uses Sub2API', () => {
    getUpstreamPriceGroups.mockResolvedValue(plazaResponse)
    renderBindings([binding()])

    expect(
      screen.queryByText('Select the upstream price group')
    ).not.toBeInTheDocument()
    expect(getUpstreamPriceGroups).not.toHaveBeenCalled()
  })

  test('adds a binding with the default markup and stores the row order', async () => {
    getUpstreamPriceGroups.mockResolvedValue(plazaResponse)
    const onChange = vi.fn()
    renderBindings([binding({ channel_id: 11 })], onChange)
    const user = userEvent.setup()

    await user.click(
      screen.getByRole('button', { name: 'Add channel binding' })
    )

    expect(onChange).toHaveBeenLastCalledWith([
      binding({ channel_id: 11 }),
      { ...binding({ channel_id: 0 }), markup_value: 0.1 },
    ])

    await user.click(screen.getAllByRole('button', { name: 'Move up' })[1])
    expect(onChange).toHaveBeenLastCalledWith([
      { ...binding({ channel_id: 0 }), markup_value: 0.1 },
      binding({ channel_id: 11 }),
    ])

    await user.click(
      screen.getAllByRole('button', { name: 'Remove binding' })[0]
    )
    expect(onChange).toHaveBeenLastCalledWith([binding({ channel_id: 11 })])
  })

  test('falls back to typing the group when the plaza cannot be read', async () => {
    getUpstreamPriceGroups.mockRejectedValue(new Error('network down'))
    const onChange = vi.fn()
    renderBindings(
      [binding({ source: 'sub2api', upstream_group: '' })],
      onChange
    )

    const input = await screen.findByPlaceholderText('Group name or ID')
    expect(
      await screen.findByText(
        'Could not read the upstream price list. Check the endpoint, then type the group name or ID.'
      )
    ).toBeInTheDocument()

    const user = userEvent.setup()
    await user.type(input, 'pro')
    expect(onChange).toHaveBeenLastCalledWith([
      binding({ source: 'sub2api', upstream_group: 'pro' }),
    ])
  })
})
