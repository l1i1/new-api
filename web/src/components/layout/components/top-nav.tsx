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
import { Link, useRouterState } from '@tanstack/react-router'
import { Menu } from 'lucide-react'
import { useMemo } from 'react'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'

import type { TopNavLink } from '../types'

type TopNavProps = React.HTMLAttributes<HTMLElement> & {
  links: TopNavLink[]
}

/**
 * 顶部导航栏组件
 * 在大屏幕显示水平导航，在小屏幕显示下拉菜单
 */
export function TopNav({ className, links, ...props }: TopNavProps) {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  // 规范化链接，确保所有可选属性都有默认值
  const normalizedLinks = useMemo(
    () =>
      links.map((link) => ({
        isActive: false,
        disabled: false,
        external: false,
        ...link,
      })),
    [links]
  )

  const isLinkActive = (link: {
    href: string
    external?: boolean
    isActive?: boolean
  }) => {
    if (link.external) return Boolean(link.isActive)
    if (link.href === '/') return pathname === '/'
    return pathname.startsWith(link.href)
  }

  return (
    <>
      {/* 移动端下拉菜单 */}
      <div className='lg:hidden'>
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger
            render={<Button size='icon' variant='outline' className='size-7' />}
          >
            <Menu />
          </DropdownMenuTrigger>
          <DropdownMenuContent side='bottom' align='start'>
            {normalizedLinks.map(
              ({ id, title, href, isActive, disabled, external, newTab }) => (
                <DropdownMenuItem
                  key={id ?? `${title}-${href}`}
                  render={
                    external || newTab ? (
                      <a
                        href={href}
                        target={(newTab ?? external) ? '_blank' : undefined}
                        rel='noopener noreferrer'
                        className={
                          isLinkActive({ href, external, isActive })
                            ? ''
                            : 'text-muted-foreground'
                        }
                      >
                        {title}
                      </a>
                    ) : (
                      <Link
                        to={href}
                        className={
                          isLinkActive({ href, external, isActive })
                            ? ''
                            : 'text-muted-foreground'
                        }
                        disabled={disabled}
                      >
                        {title}
                      </Link>
                    )
                  }
                />
              )
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* 桌面端水平导航 */}
      <nav
        className={cn(
          'hidden items-center space-x-4 lg:flex lg:space-x-4 xl:space-x-6',
          className
        )}
        {...props}
      >
        {normalizedLinks.map(
          ({ id, title, href, isActive, disabled, external, newTab }) =>
            external || newTab ? (
              <a
                key={id ?? `${title}-${href}`}
                href={href}
                target={(newTab ?? external) ? '_blank' : undefined}
                rel='noopener noreferrer'
                className={cn(
                  'hover:text-primary shrink-0 border-b-2 border-transparent text-sm font-medium whitespace-nowrap transition-colors',
                  isLinkActive({ href, external, isActive })
                    ? 'border-primary text-foreground'
                    : 'text-muted-foreground'
                )}
              >
                {title}
              </a>
            ) : (
              <Link
                key={id ?? `${title}-${href}`}
                to={href}
                disabled={disabled}
                className={cn(
                  'hover:text-primary shrink-0 border-b-2 border-transparent text-sm font-medium whitespace-nowrap transition-colors',
                  isLinkActive({ href, external, isActive })
                    ? 'border-primary text-foreground'
                    : 'text-muted-foreground'
                )}
              >
                {title}
              </Link>
            )
        )}
      </nav>
    </>
  )
}
