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
 * The payload a provider returns when it hands back a code to be rendered as a
 * QR code (e.g. WeChat Pay native returns weixin://wxpay/bizpayurl?...). It is
 * not navigable, so the UI shows it as a scannable QR instead of redirecting.
 */
export type HotPayQrCodePayload = {
  value: string
  amount: number
}

/**
 * Hook for the HotPay gateway checkout flow.
 *
 * Two checkout shapes are supported:
 * - A hosted http(s) checkout URL: redirect the same tab (window.open loses the
 *   user-gesture context across the await, so popups get blocked).
 * - A scannable code that is not a URL (WeChat native): surface it to the
 *   caller so the UI renders a QR code the buyer scans in their wallet app.
 *
 * The payment type must be a registered "hotpay:<method>" entry.
 */
export function useHotPayPayment() {
  const [processing, setProcessing] = useState(false)
  const [qrCode, setQrCode] = useState<HotPayQrCodePayload | null>(null)

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
            if (isSafeHttpCheckoutUrl(checkoutUrl)) {
              toast.success(i18next.t('Redirecting to payment page...'))
              window.location.href = checkoutUrl
              return true
            }
            // Non-URL payloads are scannable codes (weixin://…), not redirects.
            setQrCode({ value: checkoutUrl, amount: Math.floor(topupAmount) })
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

  const clearQrCode = useCallback(() => setQrCode(null), [])

  return { processing, processHotPayPayment, qrCode, clearQrCode }
}
