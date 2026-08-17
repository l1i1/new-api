/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { ArrowDown, ArrowUp, ExternalLink, Plus, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormDescription,
  FormField,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  HEADER_NAV_DEFAULT,
  isSafeHeaderNavUrl,
  moveHeaderNavItem,
  type HeaderNavItem,
  type HeaderNavModulesConfig,
  serializeHeaderNavModules,
} from './config'

const headerNavSchema = z.object({
  home: z.boolean(),
  console: z.boolean(),
  pricingEnabled: z.boolean(),
  pricingRequireAuth: z.boolean(),
  rankingsEnabled: z.boolean(),
  rankingsRequireAuth: z.boolean(),
  docs: z.boolean(),
  about: z.boolean(),
})

type HeaderNavFormValues = z.infer<typeof headerNavSchema>
type BuiltinNavId = 'console' | 'pricing' | 'rankings' | 'docs' | 'about'

const BUILTIN_NAV_IDS = new Set<BuiltinNavId>([
  'console',
  'pricing',
  'rankings',
  'docs',
  'about',
])

type HeaderNavigationSectionProps = {
  config: HeaderNavModulesConfig
  initialSerialized: string
}

const toFormValues = (config: HeaderNavModulesConfig): HeaderNavFormValues => ({
  // Home is fixed in the public navigation. Keep the field for migration
  // compatibility with older HeaderNavModules values.
  home: true,
  console:
    config.console === undefined
      ? HEADER_NAV_DEFAULT.console
      : Boolean(config.console),
  pricingEnabled:
    config.pricing?.enabled === undefined
      ? HEADER_NAV_DEFAULT.pricing.enabled
      : Boolean(config.pricing.enabled),
  pricingRequireAuth:
    config.pricing?.requireAuth === undefined
      ? HEADER_NAV_DEFAULT.pricing.requireAuth
      : Boolean(config.pricing.requireAuth),
  rankingsEnabled:
    config.rankings?.enabled === undefined
      ? HEADER_NAV_DEFAULT.rankings.enabled
      : Boolean(config.rankings.enabled),
  rankingsRequireAuth:
    config.rankings?.requireAuth === undefined
      ? HEADER_NAV_DEFAULT.rankings.requireAuth
      : Boolean(config.rankings.requireAuth),
  docs:
    config.docs === undefined ? HEADER_NAV_DEFAULT.docs : Boolean(config.docs),
  about:
    config.about === undefined
      ? HEADER_NAV_DEFAULT.about
      : Boolean(config.about),
})

const cloneItems = (items: HeaderNavItem[]): HeaderNavItem[] =>
  items.map((item) => ({ ...item }))

const createCustomNavId = (): string =>
  `custom-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`

export function HeaderNavigationSection({
  config,
  initialSerialized,
}: HeaderNavigationSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const formDefaults = useMemo(() => toFormValues(config), [config])
  const initialItems = useMemo(
    () => cloneItems(config.items ?? HEADER_NAV_DEFAULT.items),
    [config.items]
  )
  const [items, setItems] = useState<HeaderNavItem[]>(initialItems)
  const [customError, setCustomError] = useState('')

  const form = useForm<HeaderNavFormValues>({
    resolver: zodResolver(headerNavSchema),
    defaultValues: formDefaults,
  })

  useEffect(() => {
    form.reset(formDefaults)
    setItems(initialItems)
    setCustomError('')
  }, [formDefaults, form, initialItems])

  const watchedValues = form.watch()

  const getBuiltinVisible = (
    id: BuiltinNavId,
    values: HeaderNavFormValues = watchedValues
  ): boolean => {
    if (id === 'console') return values.console
    if (id === 'pricing') return values.pricingEnabled
    if (id === 'rankings') return values.rankingsEnabled
    if (id === 'docs') return values.docs
    return values.about
  }

  const setBuiltinVisible = (id: BuiltinNavId, visible: boolean) => {
    if (id === 'console') form.setValue('console', visible)
    if (id === 'pricing') form.setValue('pricingEnabled', visible)
    if (id === 'rankings') form.setValue('rankingsEnabled', visible)
    if (id === 'docs') form.setValue('docs', visible)
    if (id === 'about') form.setValue('about', visible)
  }

  const moveItem = (id: string, direction: -1 | 1) => {
    setItems((current) => moveHeaderNavItem(current, id, direction))
  }

  const addCustomItem = () => {
    const item: HeaderNavItem = {
      id: createCustomNavId(),
      title: '',
      url: '',
      newTab: false,
      visible: true,
    }
    setItems((current) => [...current, item])
    setCustomError('')
  }

  const updateCustomItem = (
    id: string,
    patch: Partial<Omit<HeaderNavItem, 'id'>>
  ) => {
    setItems((current) =>
      current.map((item) => (item.id === id ? { ...item, ...patch } : item))
    )
  }

  const removeCustomItem = (id: string) => {
    setItems((current) => current.filter((item) => item.id !== id))
  }

  const onSubmit = async (values: HeaderNavFormValues) => {
    const invalidCustomItem = items.find(
      (item) =>
        !BUILTIN_NAV_IDS.has(item.id as BuiltinNavId) &&
        (!item.title.trim() || !isSafeHeaderNavUrl(item.url))
    )
    if (invalidCustomItem) {
      setCustomError(
        t('Custom navigation items require a title and a safe URL.')
      )
      return
    }

    const normalizedItems = items.map((item) => ({
      ...item,
      visible: BUILTIN_NAV_IDS.has(item.id as BuiltinNavId)
        ? getBuiltinVisible(item.id as BuiltinNavId, values)
        : item.visible,
    }))
    const payload: HeaderNavModulesConfig = {
      ...config,
      home: true,
      console: values.console,
      docs: values.docs,
      about: values.about,
      pricing: {
        ...(config.pricing ?? HEADER_NAV_DEFAULT.pricing),
        enabled: values.pricingEnabled,
        requireAuth: values.pricingRequireAuth,
      },
      rankings: {
        ...(config.rankings ?? HEADER_NAV_DEFAULT.rankings),
        enabled: values.rankingsEnabled,
        requireAuth: values.rankingsRequireAuth,
      },
      items: normalizedItems,
    }

    const serialized = serializeHeaderNavModules(payload)
    if (serialized === initialSerialized) return

    setCustomError('')
    await updateOption.mutateAsync({
      key: 'HeaderNavModules',
      value: serialized,
    })
  }

  const resetToDefault = () => {
    form.reset(toFormValues(HEADER_NAV_DEFAULT))
    setItems(cloneItems(HEADER_NAV_DEFAULT.items))
    setCustomError('')
  }

  const builtinMeta: Record<
    BuiltinNavId,
    { title: string; description: string }
  > = {
    console: {
      title: t('Console'),
      description: t('User dashboard and quota controls.'),
    },
    pricing: {
      title: t('Model Square'),
      description: t('Public model catalog and pricing page.'),
    },
    rankings: {
      title: t('Rankings'),
      description: t('Public rankings page based on live usage data.'),
    },
    docs: {
      title: t('Docs'),
      description: t('Documentation or external knowledge base.'),
    },
    about: {
      title: t('About'),
      description: t('Static page describing the platform.'),
    },
  }

  return (
    <SettingsSection title={t('Header navigation')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            onReset={resetToDefault}
            isSaving={updateOption.isPending}
            resetLabel='Reset to default'
            saveLabel='Save navigation'
          />

          <div className='space-y-3'>
            <div className='border-border bg-muted/20 flex items-center gap-3 rounded-lg border p-3'>
              <div className='min-w-0 flex-1'>
                <div className='flex items-center gap-2'>
                  <FormLabel>{t('Home')}</FormLabel>
                  <span className='text-muted-foreground text-xs'>
                    {t('Fixed first item')}
                  </span>
                </div>
                <FormDescription>
                  {t('The home link stays first and cannot be hidden.')}
                </FormDescription>
              </div>
              <Switch checked disabled aria-label={t('Home')} />
            </div>

            {items.map((item, index) => {
              const builtin = BUILTIN_NAV_IDS.has(item.id as BuiltinNavId)
              const builtinId = item.id as BuiltinNavId
              const meta = builtin ? builtinMeta[builtinId] : null
              const visible = builtin
                ? getBuiltinVisible(builtinId)
                : item.visible
              return (
                <div
                  key={item.id}
                  className='border-border space-y-3 rounded-lg border p-3'
                >
                  <div className='flex items-start gap-3'>
                    <div className='flex min-w-0 flex-1 items-start gap-3'>
                      <div className='flex shrink-0 flex-col gap-1 pt-0.5'>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-xs'
                          disabled={index === 0}
                          aria-label={t('Move navigation item up')}
                          onClick={() => moveItem(item.id, -1)}
                        >
                          <ArrowUp />
                        </Button>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-xs'
                          disabled={index === items.length - 1}
                          aria-label={t('Move navigation item down')}
                          onClick={() => moveItem(item.id, 1)}
                        >
                          <ArrowDown />
                        </Button>
                      </div>

                      {builtin ? (
                        <div className='min-w-0 flex-1'>
                          <FormLabel>{meta?.title}</FormLabel>
                          <FormDescription>{meta?.description}</FormDescription>
                        </div>
                      ) : (
                        <div className='grid min-w-0 flex-1 gap-2 md:grid-cols-2'>
                          <div>
                            <Label
                              htmlFor={`${item.id}-title`}
                              className='sr-only'
                            >
                              {t('Title')}
                            </Label>
                            <Input
                              id={`${item.id}-title`}
                              value={item.title}
                              placeholder={t('Navigation title')}
                              onChange={(event) =>
                                updateCustomItem(item.id, {
                                  title: event.target.value,
                                })
                              }
                            />
                          </div>
                          <div>
                            <Label
                              htmlFor={`${item.id}-url`}
                              className='sr-only'
                            >
                              {t('URL')}
                            </Label>
                            <Input
                              id={`${item.id}-url`}
                              value={item.url}
                              placeholder={t('Navigation URL')}
                              onChange={(event) =>
                                updateCustomItem(item.id, {
                                  url: event.target.value,
                                })
                              }
                            />
                          </div>
                        </div>
                      )}
                    </div>

                    <div className='flex shrink-0 items-center gap-2'>
                      <span className='text-muted-foreground text-xs'>
                        {t('Visible')}
                      </span>
                      <Switch
                        checked={visible}
                        onCheckedChange={(checked) => {
                          if (builtin) {
                            setBuiltinVisible(builtinId, checked)
                          } else {
                            updateCustomItem(item.id, { visible: checked })
                          }
                        }}
                        aria-label={t('Toggle navigation item visibility')}
                      />
                      {!builtin && (
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-sm'
                          className='text-destructive hover:text-destructive'
                          aria-label={t('Delete')}
                          onClick={() => removeCustomItem(item.id)}
                        >
                          <Trash2 />
                        </Button>
                      )}
                    </div>
                  </div>

                  {builtin &&
                    (builtinId === 'pricing' || builtinId === 'rankings') && (
                      <FormField
                        control={form.control}
                        name={
                          builtinId === 'pricing'
                            ? 'pricingRequireAuth'
                            : 'rankingsRequireAuth'
                        }
                        render={({ field }) => (
                          <div className='bg-muted/20 flex items-center justify-between gap-3 rounded-md px-3 py-2'>
                            <div>
                              <FormLabel>
                                {builtinId === 'pricing'
                                  ? t('Require login to view models')
                                  : t('Require login to view rankings')}
                              </FormLabel>
                              <FormDescription>
                                {builtinId === 'pricing'
                                  ? t(
                                      'Visitors must authenticate before accessing the pricing directory.'
                                    )
                                  : t(
                                      'Visitors must authenticate before accessing the rankings page.'
                                    )}
                              </FormDescription>
                            </div>
                            <Switch
                              checked={Boolean(field.value)}
                              onCheckedChange={field.onChange}
                              disabled={!visible}
                            />
                            <FormMessage />
                          </div>
                        )}
                      />
                    )}

                  {!builtin && (
                    <label className='text-muted-foreground flex items-center gap-2 text-xs'>
                      <Checkbox
                        checked={item.newTab}
                        onCheckedChange={(checked) =>
                          updateCustomItem(item.id, {
                            newTab: checked === true,
                          })
                        }
                      />
                      {t('Open in new tab')}
                      <ExternalLink className='size-3' />
                    </label>
                  )}
                </div>
              )
            })}
          </div>

          {customError && (
            <p className='text-destructive text-sm' role='alert'>
              {customError}
            </p>
          )}

          <Button type='button' variant='outline' onClick={addCustomItem}>
            <Plus />
            {t('Add navigation item')}
          </Button>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
