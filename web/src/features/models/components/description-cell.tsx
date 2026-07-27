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
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { resolveTntContent } from '@/lib/tnt-content'

import { useModels } from './models-provider'

type DescriptionCellProps = {
  modelName: string
  description: string
}

export function DescriptionCell({
  modelName,
  description,
}: DescriptionCellProps) {
  const { i18n } = useTranslation()
  const { setOpen, setDescriptionData } = useModels()
  const localizedDescription = resolveTntContent(
    description,
    i18n.resolvedLanguage || i18n.language
  )

  if (!description) {
    return <span className='text-muted-foreground text-xs'>-</span>
  }

  const handleClick = () => {
    setDescriptionData({ modelName, description })
    setOpen('description')
  }

  return (
    <div className='max-w-[150px]'>
      <Button
        variant='link'
        onClick={handleClick}
        className='text-muted-foreground hover:text-foreground block h-auto w-full cursor-pointer overflow-hidden p-0 text-left text-sm text-ellipsis whitespace-nowrap no-underline'
      >
        {localizedDescription}
      </Button>
    </div>
  )
}
