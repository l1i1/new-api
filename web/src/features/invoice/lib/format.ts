import { formatNumber } from '@/lib/format'

/**
 * Format an invoice amount in the order's settlement currency — the currency
 * the invoice is actually issued in. This never converts between currencies:
 * the previous display folded USD orders into the site display currency, which
 * hid the settlement-currency difference behind the "mixed currency" rejection
 * and made it unexplainable to users.
 *
 * Always renders 0–2 fraction digits with the currency symbol (¥21, $1.02);
 * unknown ISO codes fall back to `CODE 1.02`, empty currency to a bare number.
 */
export function formatInvoiceAmount(amount: number, currency: string): string {
  const code = currency?.trim().toUpperCase()
  if (!code) return formatNumber(amount)
  try {
    // Fixed 'en' locale: some ICU builds render USD as "¥" under zh locales,
    // which is exactly the ambiguity this formatter exists to remove.
    // narrowSymbol keeps CNY as "¥" and USD as "$" (invoice orders are only
    // ever CNY/USD, so no cross-symbol ambiguity is introduced).
    return new Intl.NumberFormat('en', {
      style: 'currency',
      currency: code,
      currencyDisplay: 'narrowSymbol',
      minimumFractionDigits: 0,
      maximumFractionDigits: 2,
    }).format(amount)
  } catch {
    return `${code} ${amount.toFixed(2)}`
  }
}
