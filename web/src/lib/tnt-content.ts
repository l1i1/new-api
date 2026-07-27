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
const tntContentLanguages = ['zh', 'en', 'fr', 'ru', 'ja', 'vi'] as const

export type TntContentLanguage = (typeof tntContentLanguages)[number]

const tntContentLanguageSet = new Set<string>(tntContentLanguages)

type ParsedTntElement = {
  start: number
  end: number
  content: string
  language: TntContentLanguage | null
  valid: boolean
}

export function normalizeTntContentLanguage(
  value?: string | null
): TntContentLanguage | null {
  if (!value) {
    return null
  }

  const normalized = value.trim().replaceAll('_', '-').toLowerCase()
  if (!normalized) {
    return null
  }
  if (
    normalized === 'zhcn' ||
    normalized === 'zhtw' ||
    normalized === 'zh' ||
    normalized.startsWith('zh-')
  ) {
    return 'zh'
  }

  const baseLanguage = normalized.split('-', 1)[0]
  if (baseLanguage && tntContentLanguageSet.has(baseLanguage)) {
    return baseLanguage as TntContentLanguage
  }

  return null
}

function findOpeningTagEnd(source: string, start: number): number {
  let quote = ''

  for (let index = start; index < source.length; index += 1) {
    const character = source[index]
    if (quote) {
      if (character === quote) {
        quote = ''
      }
      continue
    }

    if (character === '"' || character === "'") {
      quote = character
      continue
    }
    if (character === '>') {
      return index
    }
  }

  return -1
}

function parseTntElement(source: string, start: number): ParsedTntElement {
  const openingTagEnd = findOpeningTagEnd(source, start)
  if (openingTagEnd === -1) {
    return {
      start,
      end: source.length,
      content: '',
      language: null,
      valid: false,
    }
  }

  const closingTagPattern = /<\/tnt\s*>/gi
  closingTagPattern.lastIndex = openingTagEnd + 1
  const closingTag = closingTagPattern.exec(source)
  if (!closingTag) {
    return {
      start,
      end: source.length,
      content: '',
      language: null,
      valid: false,
    }
  }

  const contentStart = openingTagEnd + 1
  const content = source.slice(contentStart, closingTag.index)
  const attributes = source.slice(start + '<tnt'.length, openingTagEnd)
  const languageAttribute = /^\s+l\s*=\s*(["'])([^"']+)\1\s*$/i.exec(attributes)
  const language = normalizeTntContentLanguage(languageAttribute?.[2])
  const hasNestedElement = /<tnt\b/i.test(content)

  return {
    start,
    end: closingTagPattern.lastIndex,
    content,
    language,
    valid: Boolean(languageAttribute && language && !hasNestedElement),
  }
}

function selectGroupContent(
  source: string,
  elements: ParsedTntElement[],
  activeLanguage: TntContentLanguage | null
): string {
  const groupStart = elements[0]?.start ?? 0
  const groupEnd = elements.at(-1)?.end ?? groupStart
  const originalGroup = source.slice(groupStart, groupEnd)
  const values = new Map<TntContentLanguage, string>()

  for (const element of elements) {
    if (!element.valid || !element.language || values.has(element.language)) {
      return originalGroup
    }
    values.set(element.language, element.content)
  }

  const selectionOrder = [activeLanguage, 'en', 'zh'].filter(
    (language, index, languages): language is TntContentLanguage =>
      Boolean(language) && languages.indexOf(language) === index
  )
  for (const language of selectionOrder) {
    const localized = values.get(language)
    if (localized !== undefined) {
      return localized
    }
  }

  return originalGroup
}

export function resolveTntContent(content: string, language?: string): string {
  const activeLanguage = normalizeTntContentLanguage(language)
  const openingTagPattern = /<tnt\b/gi
  let cursor = 0
  let output = ''

  while (cursor < content.length) {
    openingTagPattern.lastIndex = cursor
    const openingTag = openingTagPattern.exec(content)
    if (!openingTag) {
      output += content.slice(cursor)
      break
    }

    output += content.slice(cursor, openingTag.index)
    const elements = [parseTntElement(content, openingTag.index)]
    let groupEnd = elements[0].end

    while (groupEnd < content.length) {
      let nextStart = groupEnd
      while (nextStart < content.length && /\s/.test(content[nextStart])) {
        nextStart += 1
      }
      if (!/^<tnt\b/i.test(content.slice(nextStart))) {
        break
      }

      const nextElement = parseTntElement(content, nextStart)
      elements.push(nextElement)
      groupEnd = nextElement.end
    }

    output += selectGroupContent(content, elements, activeLanguage)
    cursor = groupEnd
  }

  return output
}
