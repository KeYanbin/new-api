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
import { useQueries } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type { FieldErrors } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import {
  Field,
  FieldDescription,
  FieldLabel,
  FieldError,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getUpstreamPriceGroups } from '../api'
import { DEFAULT_ENDPOINT, SUB2API_ENDPOINT } from '../models/constants'
import { getUpstreamDisplayName } from '../models/upstream-ratio-sync-helpers'
import type {
  RatioProtectChannelSource,
  UpstreamChannel,
  UpstreamPriceGroup,
} from '../types'

const sourceTypeOptions = ['newapi', 'sub2api'] as const
const markupModeOptions = ['add', 'multiply'] as const

type BindingPatch = Partial<RatioProtectChannelSource>

// Rows need a stable React identity while the administrator edits them, so each
// row carries an id that is stripped before the value is handed back.
type BindingRow = RatioProtectChannelSource & { rowId: string }

let rowIdCounter = 0

function toRows(sources: RatioProtectChannelSource[]): BindingRow[] {
  return sources.map((source) => ({
    ...source,
    rowId: `rpb_${++rowIdCounter}`,
  }))
}

function stripRows(rows: BindingRow[]): RatioProtectChannelSource[] {
  return rows.map(({ rowId: _rowId, ...source }) => source)
}

function defaultBinding(props: {
  defaultMarkupMode: RatioProtectChannelSource['markup_mode']
  defaultMarkupValue: number
}): RatioProtectChannelSource {
  return {
    channel_id: 0,
    source: 'newapi',
    endpoint: '',
    upstream_group: '',
    sync_groups: [],
    markup_mode: props.defaultMarkupMode,
    markup_value: props.defaultMarkupValue,
  }
}

type RatioProtectBindingsProps = {
  value: RatioProtectChannelSource[]
  errors?: FieldErrors<{ sources: RatioProtectChannelSource[] }>['sources']
  onChange: (value: RatioProtectChannelSource[]) => void
  channels: UpstreamChannel[]
  groupOptions: { label: string; value: string }[]
  defaultMarkupMode: RatioProtectChannelSource['markup_mode']
  defaultMarkupValue: number
}

export function RatioProtectBindings(props: RatioProtectBindingsProps) {
  const { t } = useTranslation()
  const { value, onChange, channels, groupOptions } = props
  const [rows, setRows] = useState<BindingRow[]>(() => toRows(value))
  const emittedRef = useRef(JSON.stringify(value))

  // Adopt values that arrive from outside (initial load, form reset) while
  // ignoring the echo of our own onChange.
  useEffect(() => {
    const incoming = JSON.stringify(value)
    if (incoming === emittedRef.current) return
    emittedRef.current = incoming
    setRows(toRows(value))
  }, [value])

  const emit = (next: BindingRow[]) => {
    setRows(next)
    const sources = stripRows(next)
    emittedRef.current = JSON.stringify(sources)
    onChange(sources)
  }

  // Each Sub2API binding reads the price list of its own channel, so the group
  // picker always reflects the upstream the channel really talks to.
  const groupQueries = useQueries({
    queries: rows.map((binding) => ({
      queryKey: ['upstream-price-groups', binding.channel_id, binding.endpoint],
      queryFn: async () =>
        requireServerSuccess(
          await getUpstreamPriceGroups({
            channelId: binding.channel_id,
            endpoint: binding.endpoint || undefined,
          })
        ),
      enabled: binding.source === 'sub2api' && binding.channel_id !== 0,
      staleTime: 5 * 60 * 1000,
      retry: false,
    })),
  })

  const update = (index: number, patch: BindingPatch) => {
    emit(
      rows.map((binding, position) =>
        position === index ? { ...binding, ...patch } : binding
      )
    )
  }

  const move = (index: number, offset: number) => {
    const target = index + offset
    if (target < 0 || target >= rows.length) return
    const moved = [...rows]
    const [item] = moved.splice(index, 1)
    moved.splice(target, 0, item)
    emit(moved)
  }

  const selectedChannelIds = new Set(
    rows.map((binding) => binding.channel_id).filter((id) => id !== 0)
  )

  return (
    <div className='space-y-3'>
      {rows.map((binding, index) => {
        const errors = props.errors?.[index]
        const query = groupQueries[index]
        const groups: UpstreamPriceGroup[] = query?.data?.data ?? []
        const selectedGroup = groups.find(
          (group) => group.name === binding.upstream_group
        )
        const channel = channels.find((item) => item.id === binding.channel_id)
        const channelLabel = channel
          ? getUpstreamDisplayName(channel.name, t)
          : t('Select a channel')

        let groupHint: string
        if (query?.isLoading) {
          groupHint = t('Loading upstream groups...')
        } else if (query?.isError) {
          groupHint = t(
            'Could not read the upstream price list. Check the endpoint, then type the group name or ID.'
          )
        } else if (selectedGroup) {
          groupHint = t(
            'Upstream charges {{rate}}x of the group base price for {{count}} models.',
            {
              rate: selectedGroup.rate_multiplier,
              count: selectedGroup.model_count,
            }
          )
        } else {
          groupHint = t(
            'One group is one upstream price. Loaded from the model plaza of this channel.'
          )
        }
        const endpointPlaceholder =
          binding.source === 'sub2api' ? SUB2API_ENDPOINT : DEFAULT_ENDPOINT

        return (
          <Card key={binding.rowId} className='gap-3 py-3'>
            <CardHeader className='flex items-center justify-between gap-2 px-3'>
              <div className='flex items-center gap-2'>
                <Badge variant='secondary'>{index + 1}</Badge>
                <span className='text-sm font-medium'>{channelLabel}</span>
                {binding.source === 'sub2api' && binding.upstream_group ? (
                  <Badge variant='outline'>{binding.upstream_group}</Badge>
                ) : null}
              </div>
              <div className='flex items-center gap-1'>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='h-7 w-7 p-0'
                  disabled={index === 0}
                  aria-label={t('Move up')}
                  onClick={() => move(index, -1)}
                >
                  <ArrowUp className='h-4 w-4' />
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='h-7 w-7 p-0'
                  disabled={index === rows.length - 1}
                  aria-label={t('Move down')}
                  onClick={() => move(index, 1)}
                >
                  <ArrowDown className='h-4 w-4' />
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='text-destructive h-7 w-7 p-0'
                  aria-label={t('Remove binding')}
                  onClick={() =>
                    emit(rows.filter((_, position) => position !== index))
                  }
                >
                  <Trash2 className='h-4 w-4' />
                </Button>
              </div>
            </CardHeader>
            <CardContent className='grid gap-3 px-3 sm:grid-cols-2'>
              <Field data-invalid={!!errors?.channel_id}>
                <FieldLabel htmlFor={`${binding.rowId}-channel`}>
                  {t('Local channel')}
                </FieldLabel>
                <Select
                  items={channels.map((item) => ({
                    value: String(item.id),
                    label: `${getUpstreamDisplayName(item.name, t)} (${item.id})`,
                  }))}
                  value={
                    binding.channel_id === 0 ? '' : String(binding.channel_id)
                  }
                  onValueChange={(next) =>
                    next !== null && update(index, { channel_id: Number(next) })
                  }
                >
                  <SelectTrigger
                    id={`${binding.rowId}-channel`}
                    aria-invalid={!!errors?.channel_id}
                    aria-describedby={
                      errors?.channel_id
                        ? `${binding.rowId}-channel-error`
                        : undefined
                    }
                  >
                    <SelectValue placeholder={t('Select a channel')} />
                  </SelectTrigger>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      {channels.map((item) => (
                        <SelectItem
                          key={item.id}
                          value={String(item.id)}
                          disabled={
                            item.id !== binding.channel_id &&
                            selectedChannelIds.has(item.id)
                          }
                        >
                          {getUpstreamDisplayName(item.name, t)} ({item.id})
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FieldError
                  id={`${binding.rowId}-channel-error`}
                  errors={[errors?.channel_id]}
                />
              </Field>
              <Field>
                <FieldLabel>{t('Upstream type')}</FieldLabel>
                <Select
                  items={sourceTypeOptions.map((option) => ({
                    value: option,
                    label:
                      option === 'sub2api'
                        ? t('Sub2API model plaza')
                        : t('New API pricing endpoint'),
                  }))}
                  value={binding.source}
                  onValueChange={(next) =>
                    next !== null &&
                    update(index, {
                      source: next as RatioProtectChannelSource['source'],
                      upstream_group:
                        next === 'sub2api' ? binding.upstream_group : '',
                      endpoint: '',
                    })
                  }
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      <SelectItem value='newapi'>
                        {t('New API pricing endpoint')}
                      </SelectItem>
                      <SelectItem value='sub2api'>
                        {t('Sub2API model plaza')}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </Field>
              {binding.source === 'sub2api' ? (
                <Field
                  className='sm:col-span-2'
                  data-invalid={!!errors?.upstream_group}
                >
                  <FieldLabel htmlFor={`${binding.rowId}-group`}>
                    {t('Upstream group')}
                  </FieldLabel>
                  {groups.length > 0 ? (
                    <Select
                      items={groups.map((group) => ({
                        value: group.name,
                        label: `${group.name} ×${group.rate_multiplier}`,
                      }))}
                      value={binding.upstream_group}
                      onValueChange={(next) =>
                        next !== null && update(index, { upstream_group: next })
                      }
                    >
                      <SelectTrigger
                        id={`${binding.rowId}-group`}
                        aria-invalid={!!errors?.upstream_group}
                        aria-describedby={`${binding.rowId}-group-hint${
                          errors?.upstream_group
                            ? ` ${binding.rowId}-group-error`
                            : ''
                        }`}
                      >
                        <SelectValue
                          placeholder={t('Select the upstream price group')}
                        />
                      </SelectTrigger>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          {groups.map((group) => (
                            <SelectItem key={group.id} value={group.name}>
                              {group.name} ×{group.rate_multiplier}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  ) : (
                    <Input
                      id={`${binding.rowId}-group`}
                      aria-invalid={!!errors?.upstream_group}
                      aria-describedby={`${binding.rowId}-group-hint${
                        errors?.upstream_group
                          ? ` ${binding.rowId}-group-error`
                          : ''
                      }`}
                      value={binding.upstream_group}
                      onChange={(event) =>
                        update(index, { upstream_group: event.target.value })
                      }
                      placeholder={t('Group name or ID')}
                    />
                  )}
                  <FieldDescription id={`${binding.rowId}-group-hint`}>
                    {groupHint}
                  </FieldDescription>
                  <FieldError
                    id={`${binding.rowId}-group-error`}
                    errors={[errors?.upstream_group]}
                  />
                </Field>
              ) : null}
              <Field className='sm:col-span-2'>
                <FieldLabel>{t('Upstream endpoint')}</FieldLabel>
                <Input
                  value={binding.endpoint}
                  onChange={(event) =>
                    update(index, { endpoint: event.target.value })
                  }
                  placeholder={endpointPlaceholder}
                />
                <FieldDescription>
                  {t('Leave empty to use {{endpoint}}.', {
                    endpoint: endpointPlaceholder,
                  })}
                </FieldDescription>
              </Field>
              <Field>
                <FieldLabel>{t('Markup mode')}</FieldLabel>
                <Select
                  items={markupModeOptions.map((option) => ({
                    value: option,
                    label:
                      option === 'add'
                        ? t('Add to upstream')
                        : t('Multiply upstream'),
                  }))}
                  value={binding.markup_mode}
                  onValueChange={(next) =>
                    next !== null &&
                    update(index, {
                      markup_mode:
                        next as RatioProtectChannelSource['markup_mode'],
                    })
                  }
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      <SelectItem value='add'>
                        {t('Add to upstream')}
                      </SelectItem>
                      <SelectItem value='multiply'>
                        {t('Multiply upstream')}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </Field>
              <Field data-invalid={!!errors?.markup_value}>
                <FieldLabel htmlFor={`${binding.rowId}-markup`}>
                  {t('Markup value')}
                </FieldLabel>
                <Input
                  id={`${binding.rowId}-markup`}
                  aria-invalid={!!errors?.markup_value}
                  aria-describedby={
                    errors?.markup_value
                      ? `${binding.rowId}-markup-error`
                      : undefined
                  }
                  type='number'
                  step='0.01'
                  min={0}
                  value={
                    Number.isFinite(binding.markup_value)
                      ? binding.markup_value
                      : ''
                  }
                  onChange={(event) => {
                    const next = event.target.valueAsNumber
                    if (Number.isFinite(next)) {
                      update(index, { markup_value: next })
                    }
                  }}
                />
                <FieldDescription>
                  {t('Example: upstream 0.2 with add 0.1 becomes local 0.3.')}
                </FieldDescription>
                <FieldError
                  id={`${binding.rowId}-markup-error`}
                  errors={[errors?.markup_value]}
                />
              </Field>
              <Field className='sm:col-span-2'>
                <FieldLabel>{t('Sync groups')}</FieldLabel>
                <MultiSelect
                  options={groupOptions}
                  selected={binding.sync_groups}
                  onChange={(selected) =>
                    update(index, { sync_groups: selected })
                  }
                  placeholder={t('Select groups to sync')}
                />
                <FieldDescription>
                  {t(
                    'Only follow models enabled for these local groups. Leave empty to follow every model of this upstream price.'
                  )}
                </FieldDescription>
              </Field>
            </CardContent>
          </Card>
        )
      })}
      <div className='flex items-center justify-between gap-2'>
        <Button
          type='button'
          variant='outline'
          onClick={() =>
            emit([
              ...rows,
              { ...defaultBinding(props), rowId: `rpb_${++rowIdCounter}` },
            ])
          }
        >
          <Plus className='h-4 w-4' />
          {t('Add channel binding')}
        </Button>
        <span className='text-muted-foreground text-sm'>
          {t(
            'One channel follows exactly one upstream price. The first binding wins when two bindings price the same model.'
          )}
        </span>
      </div>
    </div>
  )
}
