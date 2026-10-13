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
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type { BillingSettings } from '../../types'
import {
  RatioProtectSection,
  type RatioProtectSectionValues,
} from '../ratio-protect-section'
import { getRatioProtectSources } from '../ratio-protect-sources'

const legacy = {
  'ratio_protect_setting.channel_ids': [11, 12],
  'ratio_protect_setting.markup_mode': 'add',
  'ratio_protect_setting.markup_value': 0.1,
} as BillingSettings

function Fixture(props: { sources: RatioProtectSectionValues['sources'] }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: { retry: false },
          mutations: { retry: false },
        },
      })
  )
  return (
    <QueryClientProvider client={client}>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <RatioProtectSection
          defaultValues={{
            enabled: false,
            auto_apply: true,
            interval_minutes: 15,
            sources: props.sources,
            has_auth_token: false,
            auth_token: '',
            markup_mode: 'add',
            markup_value: 0.1,
            protect_model_ratio: true,
            protect_model_price: false,
            protect_group_ratio: false,
            skip_zero_upstream: true,
            max_change_factor: 5,
            notify: false,
          }}
        />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

async function renderSection(sources = getRatioProtectSources(legacy)) {
  const root = createRootRoute({
    component: () => <Fixture sources={sources} />,
  })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(<RouterProvider router={router} />)
  await screen.findByRole('button', { name: 'Save Changes' })
  return userEvent.setup()
}

beforeEach(() => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: [] } })
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test('saving a global setting also persists migrated legacy bindings', async () => {
  const user = await renderSection()
  await user.click(screen.getByRole('switch', { name: 'Notify on follow' }))
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'ratio_protect_setting.sources',
      value: JSON.stringify(getRatioProtectSources(legacy)),
    })
  )
})

test('removing every binding saves an explicit empty list in one request', async () => {
  const user = await renderSection()
  for (const button of screen.getAllByRole('button', {
    name: 'Remove binding',
  })) {
    await user.click(button)
  }
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Reset' })).toBeDisabled()
  )
  expect(api.put).toHaveBeenCalledTimes(1)
  expect(api.put).toHaveBeenCalledWith('/api/option/', {
    key: 'ratio_protect_setting.sources',
    value: '[]',
  })
})

test('invalid bindings display their channel and group validation errors', async () => {
  const user = await renderSection([
    {
      channel_id: 0,
      source: 'sub2api',
      endpoint: '',
      upstream_group: '',
      sync_groups: [],
      markup_mode: 'add',
      markup_value: 0.1,
    },
  ])
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(
    await screen.findByText('Select a channel for every binding')
  ).toBeVisible()
  expect(screen.getByText('Select the upstream price group')).toBeVisible()
  expect(
    screen.getByRole('combobox', { name: 'Local channel' })
  ).toHaveAttribute('aria-invalid', 'true')
  expect(
    screen.getByRole('textbox', { name: 'Upstream group' })
  ).toHaveAttribute('aria-invalid', 'true')
  expect(api.put).not.toHaveBeenCalled()
})

test('saving untouched legacy bindings completes their migration', async () => {
  const user = await renderSection()
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'ratio_protect_setting.sources',
      value: JSON.stringify(getRatioProtectSources(legacy)),
    })
  )
})
