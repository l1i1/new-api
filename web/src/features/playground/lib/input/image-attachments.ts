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
/**
 * Image attachment helpers. Attachments are downscaled and re-encoded so
 * they stay small enough to persist in browser localStorage alongside the
 * conversation they belong to.
 */

export const MAX_IMAGE_ATTACHMENTS = 4
export const MAX_IMAGE_FILE_BYTES = 15 * 1024 * 1024

const MAX_IMAGE_DIMENSION = 1024
const JPEG_QUALITY = 0.85

function loadImage(source: Blob): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(source)
    const image = new Image()
    image.onload = () => {
      URL.revokeObjectURL(url)
      resolve(image)
    }
    image.onerror = () => {
      URL.revokeObjectURL(url)
      reject(new Error('Failed to decode image'))
    }
    image.src = url
  })
}

/**
 * Convert an image file to a compressed data URL. Images larger than
 * MAX_IMAGE_DIMENSION on either axis are downscaled proportionally and
 * everything is re-encoded as JPEG to keep the stored payload small.
 */
export async function fileToImageDataUrl(file: File): Promise<string> {
  if (!file.type.startsWith('image/')) {
    throw new Error('Not an image file')
  }
  if (file.size > MAX_IMAGE_FILE_BYTES) {
    throw new Error('Image file too large')
  }

  const image = await loadImage(file)
  const scale = Math.min(
    1,
    MAX_IMAGE_DIMENSION / Math.max(image.naturalWidth, image.naturalHeight)
  )

  if (scale >= 1 && file.type === 'image/jpeg') {
    return readAsDataUrl(file)
  }

  const width = Math.max(1, Math.round(image.naturalWidth * scale))
  const height = Math.max(1, Math.round(image.naturalHeight * scale))
  const canvas = document.createElement('canvas')
  canvas.width = width
  canvas.height = height
  const context = canvas.getContext('2d')
  if (!context) {
    throw new Error('Canvas unavailable')
  }
  context.fillStyle = '#ffffff'
  context.fillRect(0, 0, width, height)
  context.drawImage(image, 0, 0, width, height)
  return canvas.toDataURL('image/jpeg', JPEG_QUALITY)
}

function readAsDataUrl(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(new Error('Failed to read image'))
    reader.readAsDataURL(blob)
  })
}

/**
 * Convert a data URL back into a File for multipart uploads. Throws when the
 * value is not a data URL (e.g. a remote URL) since those cannot be uploaded.
 */
export async function dataUrlToFile(
  dataUrl: string,
  name: string
): Promise<File> {
  if (!dataUrl.startsWith('data:')) {
    throw new Error('Only data URL images can be uploaded')
  }
  const response = await fetch(dataUrl)
  const blob = await response.blob()
  const extension = blob.type.split('/')[1] ?? 'png'
  return new File([blob], `${name}.${extension}`, { type: blob.type })
}
