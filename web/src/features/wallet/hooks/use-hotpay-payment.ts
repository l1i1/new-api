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
import i18next from 'i18next'
import { useState, useCallback } from 'react'
import { toast } from 'sonner'

import { requestHotPayPayment, isApiSuccess } from '../api'
import { getHotPayMethodFromType } from '../constants'
import {
  getCheckoutUrl,
  isSafeHttpCheckoutUrl,
} from './use-waffo-pancake-payment'

/**
 * Hook for the HotPay gateway hosted-checkout flow.
 *
 * Same-tab redirect (window.location.href) rather than window.open: the
 * user-gesture context is lost across the await, so popups get blocked.
 * The payment type must be a registered "hotpay:<method>" entry.
 */
export function useHotPayPayment() {
  const [processing, setProcessing] = useState(false)

  const processHotPayPayment = useCallback(
    async (topupAmount: number, paymentType: string) => {
      const method = getHotPayMethodFromType(paymentType)
      if (!method) {
        toast.error(i18next.t('Payment request failed'))
        return false
      }
      setProcessing(true)

      try {
        const response = await requestHotPayPayment({
          amount: Math.floor(topupAmount),
          payment_method: paymentType,
        })

        if (isApiSuccess(response)) {
          const checkoutUrl = getCheckoutUrl(response.data)

          if (checkoutUrl) {
            if (!isSafeHttpCheckoutUrl(checkoutUrl)) {
              toast.error(i18next.t('Invalid payment redirect URL'))
              return false
            }
            toast.success(i18next.t('Redirecting to payment page...'))
            window.location.href = checkoutUrl
            return true
          }
        }

        toast.error(
          (typeof response.message === 'string' &&
            response.message &&
            response.message !== 'success' &&
            response.message) ||
            i18next.t('Payment request failed')
        )
        return false
      } catch {
        toast.error(i18next.t('Payment request failed'))
        return false
      } finally {
        setProcessing(false)
      }
    },
    []
  )

  return { processing, processHotPayPayment }
}
