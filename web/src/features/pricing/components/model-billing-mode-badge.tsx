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
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'

import { getBillingModeLabelKey } from '../lib/billing-mode'
import type { PricingModel } from '../types'

interface ModelBillingModeBadgeProps {
  model: PricingModel
  className?: string
}

export function ModelBillingModeBadge(props: ModelBillingModeBadgeProps) {
  const { t } = useTranslation()
  const label = t(getBillingModeLabelKey(props.model))

  return (
    <StatusBadge
      label={label}
      variant='neutral'
      copyable={false}
      size='sm'
      type='underline'
      className={props.className}
    />
  )
}
