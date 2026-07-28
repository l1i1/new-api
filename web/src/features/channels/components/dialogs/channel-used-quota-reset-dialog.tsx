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
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'

import { handleResetChannelUsedQuota } from '../../lib'
import type { Channel } from '../../types'

interface ChannelUsedQuotaResetDialogProps {
  channel: Channel
  open: boolean
  onOpenChange: (open: boolean) => void
  canOperate: boolean
}

export function ChannelUsedQuotaResetDialog(
  props: ChannelUsedQuotaResetDialogProps
) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [isResetting, setIsResetting] = useState(false)

  return (
    <ConfirmDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Reset channel used quota')}
      desc={t(
        'Reset used quota for channel "{{name}}"? This only clears the channel usage counter and does not affect billing logs or user balances.',
        { name: props.channel.name }
      )}
      confirmText={t('Reset')}
      destructive
      disabled={!props.canOperate}
      isLoading={isResetting}
      handleConfirm={async () => {
        if (!props.canOperate || isResetting) return
        setIsResetting(true)
        try {
          const reset = await handleResetChannelUsedQuota(
            props.channel.id,
            queryClient
          )
          if (reset) props.onOpenChange(false)
        } finally {
          setIsResetting(false)
        }
      }}
    />
  )
}
