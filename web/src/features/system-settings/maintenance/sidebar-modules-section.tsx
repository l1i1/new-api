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
import { ArrowDown, ArrowUp } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormLabel,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import {
  SettingsControlChildren,
  SettingsForm,
  SettingsSwitchContent,
  SettingsControlGroup,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  SIDEBAR_MODULES_DEFAULT,
  moveSidebarItem,
  type SidebarModulesAdminConfig,
  serializeSidebarModulesAdmin,
} from './config'

type SidebarModulesSectionProps = {
  config: SidebarModulesAdminConfig
  initialSerialized: string
}

type SidebarFormValues = SidebarModulesAdminConfig

const toTitleCase = (value: string) =>
  value
    .replaceAll(/[_-]+/g, ' ')
    .replaceAll(/\b\w/g, (char) => char.toUpperCase())

const getModuleOrder = (config: SidebarModulesAdminConfig) =>
  Object.fromEntries(
    Object.entries(config).map(([sectionKey, sectionConfig]) => [
      sectionKey,
      Object.keys(sectionConfig).filter((moduleKey) => moduleKey !== 'enabled'),
    ])
  ) as Record<string, string[]>

export function SidebarModulesSection({
  config,
  initialSerialized,
}: SidebarModulesSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const sectionMeta: Record<string, { title: string; description: string }> = {
    chat: {
      title: t('Chat area'),
      description: t('Playground experiments and live conversations.'),
    },
    console: {
      title: t('Console area'),
      description: t('Dashboards, tokens, and usage analytics.'),
    },
    personal: {
      title: t('Personal area'),
      description: t('Wallet management and personal preferences.'),
    },
    admin: {
      title: t('Admin area'),
      description: t('Global configuration and administrative tools.'),
    },
  }

  const moduleMeta: Record<
    string,
    Record<string, { title: string; description: string }>
  > = {
    chat: {
      playground: {
        title: t('Playground'),
        description: t('Experiment with prompts and models in real time.'),
      },
      chat: {
        title: t('Chat'),
        description: t('Access previous conversations and start new ones.'),
      },
    },
    console: {
      detail: {
        title: t('Dashboard'),
        description: t('Aggregated usage metrics and trend charts.'),
      },
      token: {
        title: t('Token management'),
        description: t('Create, revoke, and audit API tokens.'),
      },
      log: {
        title: t('Usage logs'),
        description: t('Detailed request logs for investigations.'),
      },
      midjourney: {
        title: t('Drawing logs'),
        description: t('History of MjProxy-style image tasks.'),
      },
      task: {
        title: t('Task logs'),
        description: t('Background job tracker for queued work.'),
      },
    },
    personal: {
      topup: {
        title: t('Wallet'),
        description: t('Top up balance and view billing history.'),
      },
      invoice: {
        title: t('Invoices'),
        description: t('Allow users to view and apply for invoices.'),
      },
      personal: {
        title: t('Profile'),
        description: t('Personal settings and profile management.'),
      },
    },
    admin: {
      channel: {
        title: t('Channels'),
        description: t('Configure upstream providers and routing.'),
      },
      models: {
        title: t('Models'),
        description: t('Manage catalog visibility and pricing.'),
      },
      redemption: {
        title: t('Redeem codes'),
        description: t('Create and review invite or credit codes.'),
      },
      user: {
        title: t('Users'),
        description: t('Administer user accounts and roles.'),
      },
      setting: {
        title: t('System settings'),
        description: t('Advanced platform configuration.'),
      },
      subscription: {
        title: t('Subscription Management'),
        description: t('Manage subscription plans and pricing.'),
      },
      invoice_admin: {
        title: t('Invoice Review'),
        description: t('Review and process user invoice applications.'),
      },
    },
  }
  const formDefaults = useMemo(() => config, [config])

  const form = useForm<SidebarFormValues>({
    defaultValues: formDefaults,
  })
  const [sectionOrder, setSectionOrder] = useState(() => Object.keys(config))
  const [moduleOrder, setModuleOrder] = useState(() => getModuleOrder(config))

  useEffect(() => {
    form.reset(formDefaults)
    setSectionOrder(Object.keys(formDefaults))
    setModuleOrder(getModuleOrder(formDefaults))
  }, [formDefaults, form])

  const onSubmit = async (values: SidebarFormValues) => {
    const orderedValues: SidebarFormValues = {}
    const orderedSections = [
      ...sectionOrder,
      ...Object.keys(values).filter((key) => !sectionOrder.includes(key)),
    ]

    orderedSections.forEach((sectionKey) => {
      const section = values[sectionKey]
      if (!section) return

      const orderedSection: SidebarFormValues[string] = {
        enabled: section.enabled,
      }
      const keys = [
        ...(moduleOrder[sectionKey] ?? []),
        ...Object.keys(section).filter(
          (key) => key !== 'enabled' && !moduleOrder[sectionKey]?.includes(key)
        ),
      ]
      keys.forEach((moduleKey) => {
        if (moduleKey in section) {
          orderedSection[moduleKey] = section[moduleKey]
        }
      })
      orderedValues[sectionKey] = orderedSection
    })

    const serialized = serializeSidebarModulesAdmin(orderedValues)
    if (serialized === initialSerialized) {
      return
    }

    await updateOption.mutateAsync({
      key: 'SidebarModulesAdmin',
      value: serialized,
    })
  }

  const resetToDefault = () => {
    form.reset(SIDEBAR_MODULES_DEFAULT)
    setSectionOrder(Object.keys(SIDEBAR_MODULES_DEFAULT))
    setModuleOrder(getModuleOrder(SIDEBAR_MODULES_DEFAULT))
  }

  const sections = sectionOrder
    .map((sectionKey) => [sectionKey, config[sectionKey]] as const)
    .filter((entry): entry is readonly [string, SidebarFormValues[string]] =>
      Boolean(entry[1])
    )

  return (
    <SettingsSection title={t('Sidebar modules')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            onReset={resetToDefault}
            isSaving={updateOption.isPending}
            resetLabel='Reset to default'
            saveLabel='Save sidebar modules'
          />
          {sections.map(([sectionKey, sectionConfig], sectionIndex) => {
            const sectionInfo = sectionMeta[sectionKey] ?? {
              title: toTitleCase(sectionKey),
              description: t('Custom sidebar section'),
            }
            const sectionEnabledPath = `${sectionKey}.enabled` as const
            const modules = (
              moduleOrder[sectionKey] ?? Object.keys(sectionConfig)
            )
              .filter((moduleKey) => moduleKey !== 'enabled')
              .map(
                (moduleKey) => [moduleKey, sectionConfig[moduleKey]] as const
              )
              .filter(
                (entry): entry is readonly [string, boolean] =>
                  typeof entry[1] === 'boolean'
              )

            return (
              <SettingsControlGroup key={sectionKey}>
                <FormField
                  control={form.control}
                  name={sectionEnabledPath}
                  render={({ field }) => (
                    <SettingsSwitchItem>
                      <SettingsSwitchContent>
                        <FormLabel>{sectionInfo.title}</FormLabel>
                        <FormDescription>
                          {sectionInfo.description}
                        </FormDescription>
                      </SettingsSwitchContent>
                      <div className='flex items-center gap-1'>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-xs'
                          aria-label={`${t('Move section up')}: ${sectionInfo.title}`}
                          disabled={sectionIndex === 0}
                          onClick={() =>
                            setSectionOrder((current) =>
                              moveSidebarItem(current, sectionKey, -1)
                            )
                          }
                        >
                          <ArrowUp />
                        </Button>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-xs'
                          aria-label={`${t('Move section down')}: ${sectionInfo.title}`}
                          disabled={sectionIndex === sections.length - 1}
                          onClick={() =>
                            setSectionOrder((current) =>
                              moveSidebarItem(current, sectionKey, 1)
                            )
                          }
                        >
                          <ArrowDown />
                        </Button>
                        <FormControl>
                          <Switch
                            checked={Boolean(field.value)}
                            onCheckedChange={field.onChange}
                          />
                        </FormControl>
                      </div>
                    </SettingsSwitchItem>
                  )}
                />

                <SettingsControlChildren className='grid gap-3 md:grid-cols-2'>
                  {modules.map(([moduleKey], moduleIndex) => {
                    const moduleInfo = moduleMeta[sectionKey]?.[moduleKey] ?? {
                      title: toTitleCase(moduleKey),
                      description: t('Custom module'),
                    }
                    const modulePath = `${sectionKey}.${moduleKey}` as const
                    return (
                      <FormField
                        key={`${sectionKey}.${moduleKey}`}
                        control={form.control}
                        name={modulePath}
                        render={({ field }) => (
                          <SettingsSwitchItem className='py-2'>
                            <SettingsSwitchContent>
                              <FormLabel>{moduleInfo.title}</FormLabel>
                              <FormDescription>
                                {moduleInfo.description}
                              </FormDescription>
                            </SettingsSwitchContent>
                            <div className='flex items-center gap-1'>
                              <Button
                                type='button'
                                variant='ghost'
                                size='icon-xs'
                                aria-label={`${t('Move module up')}: ${moduleInfo.title}`}
                                disabled={moduleIndex === 0}
                                onClick={() =>
                                  setModuleOrder((current) => ({
                                    ...current,
                                    [sectionKey]: moveSidebarItem(
                                      current[sectionKey] ?? [],
                                      moduleKey,
                                      -1
                                    ),
                                  }))
                                }
                              >
                                <ArrowUp />
                              </Button>
                              <Button
                                type='button'
                                variant='ghost'
                                size='icon-xs'
                                aria-label={`${t('Move module down')}: ${moduleInfo.title}`}
                                disabled={moduleIndex === modules.length - 1}
                                onClick={() =>
                                  setModuleOrder((current) => ({
                                    ...current,
                                    [sectionKey]: moveSidebarItem(
                                      current[sectionKey] ?? [],
                                      moduleKey,
                                      1
                                    ),
                                  }))
                                }
                              >
                                <ArrowDown />
                              </Button>
                              <FormControl>
                                <Switch
                                  checked={Boolean(field.value)}
                                  onCheckedChange={field.onChange}
                                  disabled={!form.watch(sectionEnabledPath)}
                                />
                              </FormControl>
                            </div>
                          </SettingsSwitchItem>
                        )}
                      />
                    )
                  })}
                </SettingsControlChildren>
              </SettingsControlGroup>
            )
          })}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
