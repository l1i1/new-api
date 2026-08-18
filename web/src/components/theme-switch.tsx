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
import { Monitor, Moon, Sun } from 'lucide-react'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useTheme } from '@/context/theme-provider'

export function ThemeSwitch() {
  const { t } = useTranslation()
  const { resolvedTheme, setTheme, theme } = useTheme()

  let themeLabel = t('System')
  if (theme === 'light') themeLabel = t('Light')
  if (theme === 'dark') themeLabel = t('Dark')
  const accessibleLabel = `${t('Theme')}: ${themeLabel}`

  const handleToggle = () => {
    if (theme === 'light') {
      setTheme('dark')
      return
    }
    if (theme === 'dark') {
      setTheme('system')
      return
    }
    setTheme('light')
  }

  /* Update theme-color meta tag
   * when theme is updated */
  useEffect(() => {
    const themeColor = resolvedTheme === 'dark' ? '#020817' : '#fff'
    const metaThemeColor = document.querySelector("meta[name='theme-color']")
    if (metaThemeColor) metaThemeColor.setAttribute('content', themeColor)
  }, [resolvedTheme])

  let Icon = Monitor
  if (theme === 'light') Icon = Sun
  if (theme === 'dark') Icon = Moon

  return (
    <Button
      variant='ghost'
      size='icon'
      className='size-9'
      onClick={handleToggle}
      aria-label={accessibleLabel}
      title={accessibleLabel}
    >
      <Icon aria-hidden='true' className='size-[1.2rem]' />
    </Button>
  )
}
