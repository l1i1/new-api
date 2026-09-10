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
import { useState, useCallback, useEffect } from 'react'
import { toast } from 'sonner'

import {
  requestHotPayPayment,
  isApiSuccess,
  getUserBillingHistory,
} from '../api'
import { getHotPayMethodFromType } from '../constants'
import {
  getCheckoutUrl,
  isSafeHttpCheckoutUrl,
} from './use-waffo-pancake-payment'

/** How often to re-check the top-up order while the QR code is on screen. */
const PAYMENT_POLL_INTERVAL_MS = 3000
/** How long the paid state stays visible before the dialog auto-closes. */
const PAID_DIALOG_CLOSE_DELAY_MS = 2000

/**
 * The payload a provider returns when it hands back a code to be rendered as a
 * QR code (e.g. WeChat Pay native returns weixin://wxpay/bizpayurl?...). It is
 * not navigable, so the UI shows it as a scannable QR instead of redirecting.
 * The merchant order id and server-side expiry are carried along so the
 * checkout dialog can poll the local top-up record for the settlement outcome
 * and show the real order deadline.
 */
export type HotPayQrCodePayload = {
  value: string
  amount: number
  /** Merchant order id (the local top-up trade_no) used for paid detection. */
  tradeNo: string
  /** RFC3339 server-side order expiry, when exposed by the backend. */
  expiresAt?: string
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
 * For the QR flow the gateway credits asynchronously, so while the code is on
 * screen the user's latest top-up order is polled and `paid` flips to true
 * once the order matching the created order settles. `onPaid` is invoked so
 * the caller can refresh the displayed balance.
 *
 * The payment type must be a registered "hotpay:<method>" entry.
 */
export function useHotPayPayment(onPaid?: () => void) {
  const [processing, setProcessing] = useState(false)
  const [qrCode, setQrCode] = useState<HotPayQrCodePayload | null>(null)
  const [paid, setPaid] = useState(false)
  const [tradeNo, setTradeNo] = useState('')

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
          // data is typed as an object-or-string union; only the object shape
          // carries the order identity and expiry.
          const dataObject =
            response.data && typeof response.data === 'object'
              ? response.data
              : null

          if (checkoutUrl) {
            if (isSafeHttpCheckoutUrl(checkoutUrl)) {
              toast.success(i18next.t('Redirecting to payment page...'))
              window.location.href = checkoutUrl
              return true
            }
            // Non-URL payloads are scannable codes (weixin://…), not redirects.
            // order_id is the NewAPI top-up trade_no used for paid detection.
            const orderId =
              typeof dataObject?.order_id === 'string'
                ? dataObject.order_id
                : ''
            const expiresAt =
              typeof dataObject?.expires_at === 'string' &&
              dataObject.expires_at
                ? dataObject.expires_at
                : undefined
            setTradeNo(orderId)
            setQrCode({
              value: checkoutUrl,
              amount: Math.floor(topupAmount),
              tradeNo: orderId,
              expiresAt,
            })
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

  const clearQrCode = useCallback(() => {
    setQrCode(null)
    setPaid(false)
  }, [])

  // Poll the user's latest top-up orders while a QR code is on screen; the
  // gateway settles asynchronously, so this is the only paid signal the UI gets.
  useEffect(() => {
    if (!qrCode || !tradeNo || paid) return
    let closed = false

    const checkPaid = async () => {
      try {
        const response = await getUserBillingHistory(1, 5)
        if (!isApiSuccess(response) || !response.data) return
        const settled = response.data.items.some(
          (item) => item.trade_no === tradeNo && item.status === 'success'
        )
        if (settled && !closed) {
          setPaid(true)
          toast.success(i18next.t('Payment successful'))
          onPaid?.()
        }
      } catch {
        // Transient poll failures are fine; keep polling until the dialog closes.
      }
    }

    const timer = window.setInterval(checkPaid, PAYMENT_POLL_INTERVAL_MS)
    void checkPaid()

    return () => {
      closed = true
      window.clearInterval(timer)
    }
  }, [qrCode, tradeNo, paid, onPaid])

  // Keep the paid confirmation on screen briefly, then close the dialog. This
  // is a separate effect because the polling effect above tears down as soon as
  // `paid` flips, which would cancel a timer scheduled inside it.
  useEffect(() => {
    if (!paid) return
    const timer = window.setTimeout(clearQrCode, PAID_DIALOG_CLOSE_DELAY_MS)
    return () => window.clearTimeout(timer)
  }, [paid, clearQrCode])

  return {
    processing,
    processHotPayPayment,
    qrCode,
    clearQrCode,
    paid,
  }
}
