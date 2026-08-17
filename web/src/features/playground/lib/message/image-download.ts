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

const MIME_EXTENSION_MAP: Record<string, string> = {
  'image/avif': 'avif',
  'image/gif': 'gif',
  'image/jpeg': 'jpg',
  'image/png': 'png',
  'image/webp': 'webp',
}

function getImageExtension(source: string, contentType?: string): string {
  const normalizedContentType = contentType?.split(';', 1)[0]?.toLowerCase()
  const contentTypeExtension = normalizedContentType
    ? MIME_EXTENSION_MAP[normalizedContentType]
    : undefined

  if (contentTypeExtension) {
    return contentTypeExtension
  }

  const dataUrlMatch = source.match(/^data:([^;,]+)/i)
  const dataUrlExtension = dataUrlMatch?.[1]
    ? MIME_EXTENSION_MAP[dataUrlMatch[1].toLowerCase()]
    : undefined

  if (dataUrlExtension) {
    return dataUrlExtension
  }

  try {
    const pathname = new URL(source).pathname
    const extension = pathname.match(/\.([a-z0-9]{1,5})$/i)?.[1]
    if (extension) {
      return extension.toLowerCase()
    }
  } catch {
    // Invalid remote URLs are handled by the direct-anchor fallback.
  }

  return 'png'
}

function triggerDownload(url: string, filename: string, openInNewTab = false) {
  if (typeof document === 'undefined') {
    return
  }

  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.rel = 'noopener noreferrer'
  if (openInNewTab) {
    anchor.target = '_blank'
  }
  document.body.append(anchor)
  anchor.click()
  anchor.remove()
}

/**
 * Download an image result while supporting data URLs and remote image URLs.
 * Remote URLs are fetched into a same-origin blob when CORS permits it; the
 * direct-anchor fallback still lets servers that provide Content-Disposition
 * or same-origin resources download without blocking the conversation UI.
 */
export async function downloadImage(
  source: string,
  index: number
): Promise<void> {
  const normalizedSource = source.trim()
  if (!normalizedSource || typeof document === 'undefined') {
    return
  }

  const baseFilename = `generated-image-${index + 1}`
  const fallbackFilename = `${baseFilename}.${getImageExtension(normalizedSource)}`

  if (normalizedSource.startsWith('data:')) {
    triggerDownload(normalizedSource, fallbackFilename)
    return
  }

  try {
    if (typeof fetch !== 'function') {
      throw new Error('Fetch API is unavailable')
    }

    const response = await fetch(normalizedSource, { mode: 'cors' })
    if (!response.ok) {
      throw new Error(`Image request failed with status ${response.status}`)
    }

    const blob = await response.blob()
    const objectUrl = URL.createObjectURL(blob)
    const filename = `${baseFilename}.${getImageExtension(
      normalizedSource,
      blob.type
    )}`
    triggerDownload(objectUrl, filename)
    setTimeout(() => URL.revokeObjectURL(objectUrl), 0)
  } catch {
    triggerDownload(normalizedSource, fallbackFilename, true)
  }
}
