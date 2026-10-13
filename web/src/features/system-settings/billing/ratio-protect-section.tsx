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
import { getGroups } from '@/features/users/api'
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
import { safeNumberFieldProps } from '../utils/numeric-field'
import { RatioProtectBindings } from './ratio-protect-bindings'

const markupModes = ['add', 'multiply'] as const
const sourceTypes = ['newapi', 'sub2api'] as const

const channelSourceSchema = z.object({
  channel_id: z.number().int(),
  source: z.enum(sourceTypes),
  endpoint: z.string(),
  upstream_group: z.string(),
  sync_groups: z.array(z.string()),
  markup_mode: z.enum(markupModes),
  markup_value: z.coerce.number().min(0),
})

const createRatioProtectSchema = (t: (key: string) => string) =>
  z
    .object({
      ratio_protect_setting: z.object({
        enabled: z.boolean(),
        auto_apply: z.boolean(),
        interval_minutes: z.coerce.number().int().min(5).max(1440),
        sources: z.array(channelSourceSchema),
        has_auth_token: z.boolean(),
        auth_token: z.string(),
        markup_mode: z.enum(markupModes),
        markup_value: z.coerce.number().min(0),
        protect_model_ratio: z.boolean(),
        protect_model_price: z.boolean(),
        protect_group_ratio: z.boolean(),
        skip_zero_upstream: z.boolean(),
        max_change_factor: z.coerce.number().min(0),
        notify: z.boolean(),
      }),
    })
    .superRefine((data, ctx) => {
      const setting = data.ratio_protect_setting
      if (setting.enabled && setting.sources.length === 0) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['ratio_protect_setting', 'sources'],
          message: t('Select an upstream source before enabling protection'),
        })
      }
      setting.sources.forEach((source, index) => {
        if (source.channel_id === 0) {
          ctx.addIssue({
            code: z.ZodIssueCode.custom,
            path: ['ratio_protect_setting', 'sources', index, 'channel_id'],
            message: t('Select a channel for every binding'),
          })
        }
        if (
          source.source === 'sub2api' &&
          source.upstream_group.trim() === ''
        ) {
          ctx.addIssue({
            code: z.ZodIssueCode.custom,
            path: ['ratio_protect_setting', 'sources', index, 'upstream_group'],
            message: t('Select the upstream price group'),
          })
        }
      })
      if (
        !setting.protect_model_ratio &&
        !setting.protect_model_price &&
        !setting.protect_group_ratio
      ) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['ratio_protect_setting', 'protect_model_ratio'],
          message: t(
            'Protect at least model ratio, fixed price, or group ratio'
          ),
        })
      }
    })

type RatioProtectFormValues = z.infer<
  ReturnType<typeof createRatioProtectSchema>
>

export type RatioProtectSectionValues =
  RatioProtectFormValues['ratio_protect_setting']

type RatioProtectSectionProps = {
  defaultValues: RatioProtectSectionValues
}

export function RatioProtectSection({
  defaultValues,
}: RatioProtectSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const schema = createRatioProtectSchema(t)
  const { data: channelsData } = useQuery({
    queryKey: ['upstream-channels'],
    queryFn: async () => requireServerSuccess(await getUpstreamChannels()),
  })
  const { data: groupsData } = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
  })
  const channels = channelsData?.data ?? []
  const groupOptions = (groupsData?.data ?? []).map((group) => ({
    label: group,
    value: group,
  }))
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
      submitUnchanged: true,
      onSubmit: async (data, changedFields) => {
        await updateOption.mutateAsync({
          key: 'ratio_protect_setting.sources',
          value: JSON.stringify(data.ratio_protect_setting.sources),
        })
        const authToken = data.ratio_protect_setting.auth_token.trim()
        if (
          Object.hasOwn(changedFields, 'ratio_protect_setting.auth_token') &&
          authToken !== ''
        ) {
          await updateOption.mutateAsync({
            key: 'ratio_protect_setting.auth_token',
            value: authToken,
          })
        }
        for (const [key, value] of Object.entries(changedFields)) {
          if (
            key === 'ratio_protect_setting.sources' ||
            key.startsWith('ratio_protect_setting.sources.') ||
            key === 'ratio_protect_setting.channel_ids' ||
            key === 'ratio_protect_setting.channel_id' ||
            key === 'ratio_protect_setting.auth_token' ||
            key === 'ratio_protect_setting.has_auth_token'
          ) {
            continue
          }
          if (
            value === undefined ||
            value === null ||
            typeof value === 'object'
          ) {
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
  const sources = form.watch('ratio_protect_setting.sources') ?? []
  const defaultMarkupMode = form.watch('ratio_protect_setting.markup_mode')
  const defaultMarkupValue = form.watch('ratio_protect_setting.markup_value')

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
                        'Poll every bound channel upstream and keep local sell ratios at that upstream price plus markup.'
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
                    <FormLabel>
                      {t('Write local prices automatically')}
                    </FormLabel>
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
                name='ratio_protect_setting.sources'
                render={() => (
                  <FormItem className='sm:col-span-2'>
                    <FormLabel>{t('Channel bindings')}</FormLabel>
                    <RatioProtectBindings
                      value={sources}
                      errors={
                        form.formState.errors.ratio_protect_setting?.sources
                      }
                      onChange={(next) =>
                        form.setValue('ratio_protect_setting.sources', next, {
                          shouldDirty: true,
                          shouldValidate: form.formState.isSubmitted,
                        })
                      }
                      channels={channels}
                      groupOptions={groupOptions}
                      defaultMarkupMode={defaultMarkupMode}
                      defaultMarkupValue={defaultMarkupValue}
                    />
                    <FormDescription>
                      {t(
                        'Bind every channel to the upstream price it buys from. Shared models keep the first binding and are reported as conflicts.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='ratio_protect_setting.auth_token'
                render={({ field }) => (
                  <FormItem className='sm:col-span-2'>
                    <FormLabel>{t('Upstream auth token')}</FormLabel>
                    <FormControl>
                      <Input
                        type='password'
                        autoComplete='off'
                        value={field.value}
                        onChange={field.onChange}
                        placeholder={t('Enter new token to update')}
                      />
                    </FormControl>
                    <FormDescription>
                      {defaultValues.has_auth_token
                        ? t('Leave blank to keep the existing credential')
                        : t(
                            'Dashboard login token for upstream /api/pricing, or a Sub2API panel JWT when the price list needs authentication. Channel keys are not accepted by these endpoints.'
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
                    <FormLabel>{t('Default markup mode')}</FormLabel>
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
                          <SelectItem value='add'>
                            {t('Add to upstream')}
                          </SelectItem>
                          <SelectItem value='multiply'>
                            {t('Multiply upstream')}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t(
                        'Applied to bindings that do not set their own markup. Add uses local = upstream + markup, multiply uses local = upstream × markup.'
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
                    <FormLabel>{t('Default markup value')}</FormLabel>
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
                      {t(
                        'Follow the token price of each bound upstream into model_ratio.'
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
              name='ratio_protect_setting.protect_model_price'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Protect fixed price')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Follow the per-request price of each bound upstream into model_price.'
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
              name='ratio_protect_setting.protect_group_ratio'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Protect group ratio')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Follow group_ratio from /api/pricing, including groups such as gptplus.'
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
                      {t(
                        'Send a root notification when local prices are rewritten.'
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
            <div className='flex flex-wrap items-center gap-2'>
              <Button
                type='button'
                variant='outline'
                disabled={
                  !enabled || sources.length === 0 || triggerMutation.isPending
                }
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
