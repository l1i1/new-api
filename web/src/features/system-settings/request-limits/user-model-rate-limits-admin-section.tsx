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
import { useQuery } from '@tanstack/react-query'
import { Search, UserRound } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { searchUsers } from '@/features/users/api'
import { UserModelRateLimitsSection } from '@/features/users/components/user-model-rate-limits-section'
import type { User } from '@/features/users/types'

function userLabel(user: User): string {
  if (user.display_name && user.display_name !== user.username) {
    return `${user.username} (${user.display_name})`
  }
  return user.username
}

export function UserModelRateLimitsAdminSection() {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  const [selectedUser, setSelectedUser] = useState<User | null>(null)

  const query = useQuery({
    queryKey: ['admin-user-model-rate-limit-search', keyword],
    queryFn: () => searchUsers({ keyword, page_size: 20 }),
  })

  const users = query.data?.success ? (query.data.data?.items ?? []) : []

  if (selectedUser) {
    return (
      <div className='mt-8 space-y-3 border-t pt-6'>
        <div className='flex items-center justify-between gap-2'>
          <div>
            <h3 className='text-sm font-medium'>
              {t('User model rate limits')}
            </h3>
            <p className='text-muted-foreground text-xs'>
              {t('Model rate limits for user')}{' '}
              <span className='font-medium'>#{selectedUser.id}</span>{' '}
              {userLabel(selectedUser)}
            </p>
          </div>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => {
              setSelectedUser(null)
              setKeyword('')
            }}
          >
            {t('Change user')}
          </Button>
        </div>
        <UserModelRateLimitsSection userId={selectedUser.id} />
      </div>
    )
  }

  return (
    <div className='mt-8 space-y-3 border-t pt-6'>
      <div>
        <h3 className='text-sm font-medium'>
          {t('User model rate limits')}
        </h3>
        <p className='text-muted-foreground text-xs'>
          {t('Select a user to manage their model rate limits.')}
        </p>
      </div>

      <div className='max-w-sm space-y-1'>
        <Label htmlFor='user-model-rate-limit-search'>
          {t('Search users')}
        </Label>
        <div className='relative'>
          <Search className='pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 opacity-50' />
          <Input
            id='user-model-rate-limit-search'
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder={t('Search by username or display name...')}
            className='pl-9'
            autoComplete='off'
            aria-controls='user-model-rate-limit-results'
          />
        </div>
        {query.isLoading && (
          <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
        )}
        {query.isError && (
          <p className='text-destructive text-sm'>
            {t('Failed to load users')}
          </p>
        )}
        {!query.isLoading && !query.isError && users.length === 0 && (
          <p className='text-muted-foreground text-sm'>
            {t('No users found')}
          </p>
        )}
        {users.length > 0 && (
          <ul
            id='user-model-rate-limit-results'
            role='listbox'
            aria-label={t('Search users')}
            className='border-border bg-popover max-h-56 space-y-1 overflow-y-auto rounded-md border p-1'
          >
            {users.map((user) => (
              <li key={user.id} role='option'>
                <Button
                  type='button'
                  variant='ghost'
                  className='h-auto w-full justify-start px-2 py-1.5'
                  onClick={() => setSelectedUser(user)}
                >
                  <UserRound className='mr-2 size-4 shrink-0 opacity-50' />
                  <span className='truncate'>
                    #{user.id} {userLabel(user)}
                  </span>
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
