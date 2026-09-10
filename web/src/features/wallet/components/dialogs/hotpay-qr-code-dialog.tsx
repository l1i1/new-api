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
import { QRCodeSVG } from 'qrcode.react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2 } from 'lucide-react'
import { useMemo } from 'react'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'

interface HotPayQrCodeDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Scannable code payload (e.g. weixin://wxpay/bizpayurl?pr=…). */
  value: string
  /** Top-up amount in quota units, shown for buyer confirmation. */
  amount: number
  /** True once the order settled; flips the dialog to a paid confirmation. */
  paid?: boolean
  /** RFC3339 server-side order deadline; drives the expiry hint when present. */
  expiresAt?: string
}

/**
 * Renders a provider-issued payment code as a scannable QR code. Used for
 * checkout methods that return a code instead of a hosted page, such as the
 * WeChat Pay native flow whose payload is a weixin:// URL. While open, the
 * order status is polled upstream and the dialog switches to a paid state on
 * settlement.
 */
export function HotPayQrCodeDialog({
  open,
  onOpenChange,
  value,
  amount,
  paid = false,
  expiresAt,
}: HotPayQrCodeDialogProps) {
  const { t } = useTranslation()
  const expiryTime = useMemo(() => {
    if (!expiresAt) return null
    const parsed = new Date(expiresAt)
    return Number.isNaN(parsed.getTime()) ? null : parsed
  }, [expiresAt])
  const expiryLabel = useMemo(() => {
    if (!expiryTime) return null
    return expiryTime.toLocaleTimeString([], {
      hour: '2-digit',
      minute: '2-digit',
    })
  }, [expiryTime])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-md'>
        {paid ? (
          <>
            <DialogHeader>
              <DialogTitle className='text-center'>
                {t('Payment successful')}
              </DialogTitle>
              <DialogDescription className='text-center'>
                {t(
                  'The payment has been received and your balance has been updated.'
                )}
              </DialogDescription>
            </DialogHeader>

            <div className='flex justify-center py-6'>
              <CheckCircle2 className='h-16 w-16 text-emerald-500' />
            </div>

            <p className='text-foreground text-center text-base font-medium'>
              {t('Top-up amount: {{amount}}', { amount })}
            </p>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t('Scan to pay')}</DialogTitle>
              <DialogDescription>
                {t(
                  'Scan this QR code with WeChat to complete the payment.'
                )}
              </DialogDescription>
            </DialogHeader>

            <div className='flex justify-center rounded-lg bg-white p-4'>
              <QRCodeSVG value={value} size={220} />
            </div>

            <div className='space-y-1 text-center'>
              <p className='text-foreground text-base font-medium'>
                {t('Top-up amount: {{amount}}', { amount })}
              </p>
              {expiryLabel && (
                <p className='text-muted-foreground text-sm'>
                  {t('Order expires at {{time}}', { time: expiryLabel })}
                </p>
              )}
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Your balance will update automatically once the payment completes.'
                )}
              </p>
            </div>
          </>
        )}

        <DialogFooter>
          <Button type='button' onClick={() => onOpenChange(false)}>
            {t('Close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
