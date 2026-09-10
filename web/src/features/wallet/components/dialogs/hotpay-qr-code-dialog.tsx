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
}

/**
 * Renders a provider-issued payment code as a scannable QR code. Used for
 * checkout methods that return a code instead of a hosted page, such as the
 * WeChat Pay native flow whose payload is a weixin:// URL.
 */
export function HotPayQrCodeDialog({
  open,
  onOpenChange,
  value,
  amount,
}: HotPayQrCodeDialogProps) {
  const { t } = useTranslation()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-md'>
        <DialogHeader>
          <DialogTitle>{t('Scan to pay')}</DialogTitle>
          <DialogDescription>
            {t(
              'Scan this QR code with WeChat to complete the payment. The order expires in 45 minutes.'
            )}
          </DialogDescription>
        </DialogHeader>

        <div className='flex justify-center rounded-lg bg-white p-4'>
          <QRCodeSVG value={value} size={220} />
        </div>

        <p className='text-muted-foreground text-center text-sm'>
          {t('Top-up amount: {{amount}}', { amount })}
        </p>

        <DialogFooter>
          <Button type='button' onClick={() => onOpenChange(false)}>
            {t('Close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
