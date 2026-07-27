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
import { Link } from '@tanstack/react-router'
import { ArrowRight, BookOpen, Check, KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'

import { HeroTerminalDemo } from '../hero-terminal-demo'

interface HeroProps {
  className?: string
  isAuthenticated?: boolean
}

export function Hero(props: HeroProps) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const docsUrl =
    (status?.docs_link as string | undefined) || 'https://docs.newapi.pro'

  const renderDocsButton = () => {
    const buttonContent = (
      <>
        <BookOpen className='size-4' aria-hidden='true' />
        <span>{t('Docs')}</span>
      </>
    )

    if (docsUrl.startsWith('http')) {
      return (
        <Button
          variant='outline'
          className='h-11 rounded-lg px-5'
          render={
            <a href={docsUrl} target='_blank' rel='noopener noreferrer' />
          }
        >
          {buttonContent}
        </Button>
      )
    }

    return (
      <Button
        variant='outline'
        className='h-11 rounded-lg px-5'
        render={<Link to={docsUrl} />}
      >
        {buttonContent}
      </Button>
    )
  }

  return (
    <section className='border-border/40 relative z-10 flex min-h-[calc(100svh-7rem)] items-center overflow-hidden border-b px-6 py-12 md:py-16'>
      <div
        aria-hidden='true'
        inert
        className='pointer-events-none absolute inset-y-6 right-[-8rem] left-[44%] hidden items-center overflow-hidden opacity-[0.16] md:flex dark:opacity-25'
      >
        <HeroTerminalDemo className='max-w-3xl' />
      </div>

      <div className='relative mx-auto w-full max-w-6xl'>
        <div className='flex max-w-xl flex-col items-start'>
          <div
            className='landing-animate-fade-up text-primary mb-4 flex items-center gap-2 text-xs font-semibold opacity-0'
            style={{ animationDelay: '0ms' }}
          >
            <span
              className='bg-primary size-1.5 rounded-full'
              aria-hidden='true'
            />
            <span>{t('home.hero.kicker')}</span>
          </div>

          <h1
            className='landing-animate-fade-up text-4xl leading-[1.08] font-bold opacity-0 sm:text-5xl lg:text-6xl'
            style={{ animationDelay: '60ms' }}
          >
            <span className='block'>Tokeness</span>
            <span className='text-foreground/72 mt-2 block text-3xl sm:text-4xl lg:text-5xl'>
              {t('home.hero.title')}
            </span>
          </h1>

          <p
            className='landing-animate-fade-up text-muted-foreground mt-5 max-w-xl text-base leading-relaxed opacity-0'
            style={{ animationDelay: '120ms' }}
          >
            {t('home.hero.copy')}
          </p>

          <div
            className='landing-animate-fade-up mt-7 flex flex-wrap items-center gap-3 opacity-0'
            style={{ animationDelay: '180ms' }}
          >
            <Button
              className='group h-11 rounded-lg px-5'
              render={
                <Link to={props.isAuthenticated ? '/dashboard' : '/sign-up'} />
              }
            >
              {props.isAuthenticated ? t('Go to Dashboard') : t('Get Started')}
              <ArrowRight
                className='size-4 transition-transform duration-200 group-hover:translate-x-0.5'
                aria-hidden='true'
              />
            </Button>
            <Button
              variant='outline'
              className='h-11 rounded-lg px-5'
              render={<Link to='/pricing' />}
            >
              {t('View Pricing')}
            </Button>
            {renderDocsButton()}
          </div>

          <div
            className='landing-animate-fade-up border-border/60 text-muted-foreground mt-7 flex max-w-xl items-start gap-3 border-t pt-4 text-sm leading-relaxed opacity-0'
            style={{ animationDelay: '240ms' }}
          >
            <KeyRound
              className='text-foreground mt-0.5 size-4 shrink-0'
              aria-hidden='true'
            />
            <span>{t('home.hero.protocols')}</span>
            <Check
              className='text-primary mt-0.5 size-4 shrink-0'
              aria-hidden='true'
            />
          </div>
        </div>
      </div>
    </section>
  )
}
