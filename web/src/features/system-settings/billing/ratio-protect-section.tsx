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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery } from '@tanstack/react-query'
import type { Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getUpstreamChannels, triggerRatioProtectTask } from '../api'
import { FormDirtyIndicator } from '../components/form-dirty-indicator'
import { FormNavigationGuard } from '../components/form-navigation-guard'
import {
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useSettingsForm } from '../hooks/use-settings-form'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  DEFAULT_ENDPOINT,
  MODELS_DEV_PRESET_ENDPOINT,
  MODELS_DEV_PRESET_ID,
  OFFICIAL_CHANNEL_ENDPOINT,
  OFFICIAL_CHANNEL_ID,
  OPENROUTER_CHANNEL_TYPE,
  OPENROUTER_ENDPOINT,
} from '../models/constants'
import { getUpstreamDisplayName } from '../models/upstream-ratio-sync-helpers'
import { safeNumberFieldProps } from '../utils/numeric-field'

const markupModes = ['add', 'multiply'] as const

const createRatioProtectSchema = (t: (key: string) => string) =>
  z
    .object({
      ratio_protect_setting: z.object({
        enabled: z.boolean(),
        auto_apply: z.boolean(),
        interval_minutes: z.coerce.number().int().min(5).max(1440),
        channel_id: z.coerce.number().int(),
        endpoint: z.string(),
        markup_mode: z.enum(markupModes),
        markup_value: z.coerce.number().min(0),
        protect_model_ratio: z.boolean(),
        protect_model_price: z.boolean(),
        skip_zero_upstream: z.boolean(),
        max_change_factor: z.coerce.number().min(0),
        notify: z.boolean(),
      }),
    })
    .superRefine((data, ctx) => {
      const setting = data.ratio_protect_setting
      if (setting.enabled && setting.channel_id === 0) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['ratio_protect_setting', 'channel_id'],
          message: t('Select an upstream source before enabling protection'),
        })
      }
      if (!setting.protect_model_ratio && !setting.protect_model_price) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['ratio_protect_setting', 'protect_model_ratio'],
          message: t('Protect at least model ratio or fixed price'),
        })
      }
    })

type RatioProtectFormValues = z.infer<ReturnType<typeof createRatioProtectSchema>>

export type RatioProtectSectionValues = RatioProtectFormValues['ratio_protect_setting']

function defaultEndpointForChannel(channelId: number, channelType?: number) {
  if (channelId === OFFICIAL_CHANNEL_ID) return OFFICIAL_CHANNEL_ENDPOINT
  if (channelId === MODELS_DEV_PRESET_ID) return MODELS_DEV_PRESET_ENDPOINT
  if (channelType === OPENROUTER_CHANNEL_TYPE) return OPENROUTER_ENDPOINT
  return DEFAULT_ENDPOINT
}

type RatioProtectSectionProps = {
  defaultValues: RatioProtectSectionValues
}

export function RatioProtectSection({ defaultValues }: RatioProtectSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const schema = createRatioProtectSchema(t)
  const { data: channelsData } = useQuery({
    queryKey: ['upstream-channels'],
    queryFn: async () => requireServerSuccess(await getUpstreamChannels()),
  })
  const channels = channelsData?.data ?? []
  const triggerMutation = useMutation({
    mutationFn: async () =>
      requireServerSuccess(await triggerRatioProtectTask()),
    onSuccess: () => {
      toast.success(t('Ratio protection task started'))
    },
    onError: (error: Error) => {
      handleServerError(error, t('Failed to start ratio protection task'))
    },
  })

  const { form, handleSubmit, handleReset, isDirty, isSubmitting } =
    useSettingsForm<RatioProtectFormValues>({
      resolver: zodResolver(schema) as Resolver<
        RatioProtectFormValues,
        unknown,
        RatioProtectFormValues
      >,
      defaultValues: { ratio_protect_setting: defaultValues },
      onSubmit: async (_data, changedFields) => {
        for (const [key, value] of Object.entries(changedFields)) {
          if (value === undefined || value === null || typeof value === 'object') {
            continue
          }
          await updateOption.mutateAsync({
            key,
            value: value as string | number | boolean,
          })
        }
      },
    })

  const enabled = form.watch('ratio_protect_setting.enabled')
  const selectedChannelId = form.watch('ratio_protect_setting.channel_id')

  return (
    <>
      <FormNavigationGuard when={isDirty} />
      <SettingsSection title={t('Upstream Ratio Protection')}>
        <Form {...form}>
          <SettingsForm onSubmit={handleSubmit}>
            <SettingsPageFormActions
              onSave={handleSubmit}
              onReset={handleReset}
              isSaving={updateOption.isPending || isSubmitting}
              isResetDisabled={!isDirty}
            />
            <FormDirtyIndicator isDirty={isDirty} />
            <FormField
              control={form.control}
              name='ratio_protect_setting.enabled'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Enable automatic follow')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Poll one upstream source and keep local sell ratios at upstream plus markup.'
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            <FormField
              control={form.control}
              name='ratio_protect_setting.auto_apply'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Write local prices automatically')}</FormLabel>
                    <FormDescription>
                      {t(
                        'When the upstream value itself changes, overwrite local with upstream plus markup. Manual local edits are kept until that happens.'
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='ratio_protect_setting.channel_id'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Listen source')}</FormLabel>
                    <Select
                      items={[
                        { value: '0', label: t('Select a channel') },
                        ...channels.map((channel) => ({
                          value: String(channel.id),
                          label: getUpstreamDisplayName(channel.name, t),
                        })),
                      ]}
                      value={String(field.value)}
                      onValueChange={(value) => {
                        const channelId = Number(value)
                        field.onChange(channelId)
                        const channel = channels.find((item) => item.id === channelId)
                        form.setValue(
                          'ratio_protect_setting.endpoint',
                          defaultEndpointForChannel(channelId, channel?.type),
                          { shouldDirty: true }
                        )
                      }}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='0'>{t('Select a channel')}</SelectItem>
                          {channels.map((channel) => (
                            <SelectItem key={channel.id} value={String(channel.id)}>
                              {getUpstreamDisplayName(channel.name, t)}
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t(
                        'Only one source is used. Changing the source resets the last-seen snapshot.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='ratio_protect_setting.endpoint'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Upstream endpoint')}</FormLabel>
                    <FormControl>
                      <Input
                        value={field.value}
                        onChange={field.onChange}
                        placeholder={DEFAULT_ENDPOINT}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Use /api/pricing, /api/ratio_config, openrouter, or a full URL.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='ratio_protect_setting.markup_mode'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Markup mode')}</FormLabel>
                    <Select
                      items={[
                        { value: 'add', label: t('Add to upstream') },
                        { value: 'multiply', label: t('Multiply upstream') },
                      ]}
                      value={field.value}
                      onValueChange={field.onChange}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='add'>{t('Add to upstream')}</SelectItem>
                          <SelectItem value='multiply'>
                            {t('Multiply upstream')}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t(
                        'Add uses local = upstream + markup. Multiply uses local = upstream × markup.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='ratio_protect_setting.markup_value'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Markup value')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        step='0.01'
                        min={0}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Example: upstream 0.2 with add 0.1 becomes local 0.3.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='ratio_protect_setting.interval_minutes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Check interval (minutes)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={5}
                        max={1440}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Minimum interval is 5 minutes.')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='ratio_protect_setting.max_change_factor'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Anomaly jump factor')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        step='0.1'
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Skip writes when upstream jumps more than this factor. Use 0 to disable.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
            <FormField
              control={form.control}
              name='ratio_protect_setting.protect_model_ratio'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Protect model ratio')}</FormLabel>
                    <FormDescription>
                      {t('Follow token-based model_ratio from the listen source.')}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            <FormField
              control={form.control}
              name='ratio_protect_setting.protect_model_price'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Protect fixed price')}</FormLabel>
                    <FormDescription>
                      {t('Follow request-based model_price from the listen source.')}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            <FormField
              control={form.control}
              name='ratio_protect_setting.skip_zero_upstream'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Skip zero upstream prices')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Do not follow an upstream value of 0, which is often a free or placeholder price.'
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            <FormField
              control={form.control}
              name='ratio_protect_setting.notify'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Notify on follow')}</FormLabel>
                    <FormDescription>
                      {t('Send a root notification when local prices are rewritten.')}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
            <div className='flex flex-wrap items-center gap-2'>
              <Button
                type='button'
                variant='outline'
                disabled={!enabled || selectedChannelId === 0 || triggerMutation.isPending}
                onClick={() => triggerMutation.mutate()}
              >
                {t('Run protection now')}
              </Button>
            </div>
          </SettingsForm>
        </Form>
      </SettingsSection>
    </>
  )
}
