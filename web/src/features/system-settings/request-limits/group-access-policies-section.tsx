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
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { AlertTriangle, Plus, Save, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import {
  getChannel,
  getChannels,
  searchChannels,
} from '@/features/channels/api'
import { getGroups } from '@/features/users/api'
import { useDebounce } from '@/hooks/use-debounce'

import {
  getGroupAccessPolicy,
  replaceGroupAccessPolicy,
  type GroupAccessPolicyInput,
} from './group-access-policies-api'

const emptyPolicy = (): GroupAccessPolicyInput => ({
  blocked_channel_ids: [],
  blocked_models: [],
  blocked_groups: [],
  content_moderation_disabled: false,
})

export function GroupAccessPoliciesSection() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [selectedGroup, setSelectedGroup] = useState('')
  const [policy, setPolicy] = useState<GroupAccessPolicyInput>(emptyPolicy)
  const [modelsText, setModelsText] = useState('')
  const [channelSearch, setChannelSearch] = useState('')
  const debouncedChannelSearch = useDebounce(channelSearch, 200)

  const groupsQuery = useQuery({
    queryKey: ['group-access-policy-groups'],
    queryFn: () => getGroups(),
  })
  const groupOptions = useMemo(
    () => groupsQuery.data?.data ?? [],
    [groupsQuery.data]
  )

  useEffect(() => {
    if (!selectedGroup && groupOptions.length > 0) {
      setSelectedGroup(groupOptions[0])
    }
  }, [groupOptions, selectedGroup])

  const policyQuery = useQuery({
    queryKey: ['group-access-policy', selectedGroup],
    queryFn: () => getGroupAccessPolicy(selectedGroup),
    enabled: Boolean(selectedGroup),
  })

  useEffect(() => {
    setChannelSearch('')
  }, [selectedGroup])

  useEffect(() => {
    const loadedPolicy = policyQuery.data?.data
    if (
      !policyQuery.data?.success ||
      !loadedPolicy ||
      loadedPolicy.group_name !== selectedGroup
    ) {
      setPolicy(emptyPolicy())
      setModelsText('')
      return
    }

    setPolicy({
      blocked_channel_ids: loadedPolicy.blocked_channel_ids,
      blocked_models: loadedPolicy.blocked_models,
      blocked_groups: loadedPolicy.blocked_groups,
      content_moderation_disabled: loadedPolicy.content_moderation_disabled,
    })
    setModelsText(loadedPolicy.blocked_models.join('\n'))
  }, [policyQuery.data, selectedGroup])

  const selectedChannelQueries = useQueries({
    queries: policy.blocked_channel_ids.map((channelId) => ({
      queryKey: ['group-access-policy-channel', channelId],
      queryFn: () => getChannel(channelId),
      retry: false,
      staleTime: 60_000,
    })),
  })

  const channelsQuery = useQuery({
    queryKey: ['group-access-policy-channel-search', debouncedChannelSearch],
    queryFn: () =>
      debouncedChannelSearch.trim()
        ? searchChannels({
            keyword: debouncedChannelSearch.trim(),
            p: 1,
            page_size: 20,
            id_sort: true,
          })
        : getChannels({ p: 1, page_size: 20, id_sort: true }),
    enabled: Boolean(selectedGroup),
  })
  const channelOptions = channelsQuery.data?.data?.items ?? []

  const saveMutation = useMutation({
    mutationFn: (nextPolicy: GroupAccessPolicyInput) =>
      replaceGroupAccessPolicy(selectedGroup, nextPolicy),
    onSuccess: async (result) => {
      if (!result.success || !result.data) {
        toast.error(result.message || t('Failed to save group access policy'))
        return
      }
      await queryClient.invalidateQueries({
        queryKey: ['group-access-policy', selectedGroup],
      })
      toast.success(t('Group access policy saved'))
    },
    onError: () => toast.error(t('Failed to save group access policy')),
  })

  const toggleBlockedGroup = (groupName: string, checked: boolean) => {
    setPolicy((current) => ({
      ...current,
      blocked_groups: checked
        ? [...current.blocked_groups, groupName]
        : current.blocked_groups.filter((item) => item !== groupName),
    }))
  }

  const addBlockedChannel = (channelId: number) => {
    setPolicy((current) => {
      if (current.blocked_channel_ids.includes(channelId)) return current
      return {
        ...current,
        blocked_channel_ids: [...current.blocked_channel_ids, channelId],
      }
    })
  }

  const removeBlockedChannel = (channelId: number) => {
    setPolicy((current) => ({
      ...current,
      blocked_channel_ids: current.blocked_channel_ids.filter(
        (item) => item !== channelId
      ),
    }))
  }

  const savePolicy = () => {
    const blockedModels = [
      ...new Set(
        modelsText
          .split('\n')
          .map((model) => model.trim())
          .filter(Boolean)
      ),
    ]
    saveMutation.mutate({ ...policy, blocked_models: blockedModels })
  }

  const isUnavailable =
    !selectedGroup || policyQuery.isLoading || saveMutation.isPending

  return (
    <div className='mt-8 space-y-5 border-t pt-6'>
      <div>
        <h3 className='text-sm font-medium'>{t('Group access policies')}</h3>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Apply channel, model, target group, and moderation restrictions to every user in a base group.'
          )}
        </p>
      </div>

      <div className='max-w-sm space-y-1'>
        <Label htmlFor='group-access-policy-subject-group'>
          {t('Base user group')}
        </Label>
        <NativeSelect
          id='group-access-policy-subject-group'
          className='w-full'
          value={selectedGroup}
          onChange={(event) => setSelectedGroup(event.target.value)}
          disabled={groupsQuery.isLoading || groupOptions.length === 0}
        >
          <NativeSelectOption value='' disabled>
            {t('Select a group')}
          </NativeSelectOption>
          {groupOptions.map((groupName) => (
            <NativeSelectOption key={groupName} value={groupName}>
              {groupName}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      {groupsQuery.isError && (
        <p className='text-destructive text-sm'>{t('Failed to load groups')}</p>
      )}
      {policyQuery.isError || policyQuery.data?.success === false ? (
        <p className='text-destructive text-sm'>
          {policyQuery.data?.message || t('Failed to load group access policy')}
        </p>
      ) : null}

      <fieldset className='space-y-3' disabled={isUnavailable}>
        <legend className='text-sm font-medium'>{t('Blocked channels')}</legend>
        <p className='text-muted-foreground text-xs'>
          {t(
            'These channels cannot be selected by new requests from this group.'
          )}
        </p>
        <div className='flex flex-wrap gap-2'>
          {policy.blocked_channel_ids.map((channelId, index) => {
            const channelResult = selectedChannelQueries[index]?.data
            const channel = channelResult?.success
              ? channelResult.data
              : undefined
            const isStale = selectedChannelQueries[index]?.isSuccess && !channel
            return (
              <Badge
                key={channelId}
                variant={isStale ? 'warning' : 'outline'}
                title={isStale ? t('This channel no longer exists') : undefined}
              >
                {channel ? `${channel.name} (#${channelId})` : `#${channelId}`}
                {isStale && <span>{t('Stale')}</span>}
                <button
                  type='button'
                  aria-label={t('Remove channel {{id}}', { id: channelId })}
                  onClick={() => removeBlockedChannel(channelId)}
                >
                  <X aria-hidden='true' />
                </button>
              </Badge>
            )
          })}
          {policy.blocked_channel_ids.length === 0 && (
            <span className='text-muted-foreground text-sm'>
              {t('No blocked channels')}
            </span>
          )}
        </div>
        <Input
          aria-label={t('Search channels')}
          placeholder={t('Search channels by ID or name')}
          value={channelSearch}
          onChange={(event) => setChannelSearch(event.target.value)}
        />
        <div className='max-h-48 space-y-1 overflow-y-auto rounded-md border p-2'>
          {channelOptions.map((channel) => {
            const isSelected = policy.blocked_channel_ids.includes(channel.id)
            return (
              <div
                key={channel.id}
                className='flex items-center justify-between gap-2 rounded px-2 py-1.5 text-sm'
              >
                <span className='min-w-0 truncate'>
                  {channel.name}{' '}
                  <span className='text-muted-foreground'>#{channel.id}</span>
                </span>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  disabled={isSelected}
                  onClick={() => addBlockedChannel(channel.id)}
                >
                  <Plus aria-hidden='true' />
                  {isSelected ? t('Added') : t('Block')}
                </Button>
              </div>
            )
          })}
          {!channelsQuery.isLoading && channelOptions.length === 0 && (
            <p className='text-muted-foreground px-2 py-1 text-sm'>
              {t('No channels found')}
            </p>
          )}
        </div>
      </fieldset>

      <div className='space-y-2'>
        <Label htmlFor='group-access-policy-blocked-models'>
          {t('Blocked models')}
        </Label>
        <Textarea
          id='group-access-policy-blocked-models'
          value={modelsText}
          onChange={(event) => setModelsText(event.target.value)}
          placeholder={'gpt-5.4\nclaude-opus-4-6'}
          disabled={isUnavailable}
        />
        <p className='text-muted-foreground text-xs'>
          {t(
            'Enter one exact model name per line. Wildcards are not supported.'
          )}
        </p>
      </div>

      <fieldset className='space-y-2' disabled={isUnavailable}>
        <legend className='text-sm font-medium'>
          {t('Blocked target groups')}
        </legend>
        <p className='text-muted-foreground text-xs'>
          {t(
            'These restrictions are applied after the existing usable-group configuration.'
          )}
        </p>
        <div className='grid gap-2 sm:grid-cols-2 lg:grid-cols-3'>
          {groupOptions.map((groupName) => (
            <label
              key={groupName}
              className='flex items-center gap-2 rounded-md border px-3 py-2 text-sm'
            >
              <Checkbox
                checked={policy.blocked_groups.includes(groupName)}
                onCheckedChange={(checked) =>
                  toggleBlockedGroup(groupName, checked === true)
                }
              />
              <span className='truncate'>{groupName}</span>
            </label>
          ))}
        </div>
      </fieldset>

      <Alert
        variant={policy.content_moderation_disabled ? 'destructive' : 'default'}
      >
        <AlertTriangle aria-hidden='true' />
        <AlertTitle>{t('Platform AI moderation exemption')}</AlertTitle>
        <AlertDescription>
          {t(
            'When enabled, this group skips platform AI moderation only. Local sensitive-word checks, upstream cyber policy, and provider safety controls still apply.'
          )}
        </AlertDescription>
        <label className='col-start-2 mt-2 flex items-center gap-2 text-sm'>
          <Switch
            checked={policy.content_moderation_disabled}
            onCheckedChange={(checked) =>
              setPolicy((current) => ({
                ...current,
                content_moderation_disabled: checked,
              }))
            }
            disabled={isUnavailable}
          />
          {t('Disable platform AI moderation for this group')}
        </label>
      </Alert>

      <Button type='button' onClick={savePolicy} disabled={isUnavailable}>
        <Save aria-hidden='true' />
        {saveMutation.isPending
          ? t('Saving...')
          : t('Save group access policy')}
      </Button>
    </div>
  )
}
