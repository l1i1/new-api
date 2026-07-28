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
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'

const HOME_METADATA = [
  {
    selector: 'meta[name="title"]',
    attribute: 'name',
    attributeValue: 'title',
    contentKey: 'title',
  },
  {
    selector: 'meta[name="description"]',
    attribute: 'name',
    attributeValue: 'description',
    contentKey: 'description',
  },
  {
    selector: 'meta[name="keywords"]',
    attribute: 'name',
    attributeValue: 'keywords',
    contentKey: 'keywords',
  },
  {
    selector: 'meta[property="og:title"]',
    attribute: 'property',
    attributeValue: 'og:title',
    contentKey: 'title',
  },
  {
    selector: 'meta[property="og:description"]',
    attribute: 'property',
    attributeValue: 'og:description',
    contentKey: 'description',
  },
  {
    selector: 'meta[name="twitter:title"]',
    attribute: 'name',
    attributeValue: 'twitter:title',
    contentKey: 'title',
  },
  {
    selector: 'meta[name="twitter:description"]',
    attribute: 'name',
    attributeValue: 'twitter:description',
    contentKey: 'description',
  },
] as const

export function useHomeMetadata(systemName?: string) {
  const { i18n, t } = useTranslation()

  useEffect(() => {
    const previousTitle = document.title
    const values = {
      title: t('home.meta.title'),
      description: t('home.meta.description'),
      keywords: t('home.meta.keywords'),
    }
    const previousMetadata = HOME_METADATA.map((item) => {
      const existing = document.querySelector<HTMLMetaElement>(item.selector)
      const element = existing ?? document.createElement('meta')

      if (!existing) {
        element.setAttribute(item.attribute, item.attributeValue)
        document.head.append(element)
      }

      const previousContent = element.content
      element.content = values[item.contentKey]
      return { definition: item, element, previousContent, created: !existing }
    })

    document.title = t('home.meta.title')

    return () => {
      document.title = systemName || previousTitle
      for (const item of previousMetadata) {
        if (item.created) {
          item.element.remove()
        } else if (
          item.definition.selector === 'meta[name="title"]' &&
          systemName
        ) {
          item.element.content = systemName
        } else {
          item.element.content = item.previousContent
        }
      }
    }
  }, [i18n.resolvedLanguage, systemName, t])
}
