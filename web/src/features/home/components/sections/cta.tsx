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
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'
import { Button } from '@/components/ui/button'

interface CTAProps {
  className?: string
  isAuthenticated?: boolean
}

export function CTA(props: CTAProps) {
  const { t } = useTranslation()

  return (
    <section className='relative z-10 px-6 py-20 md:py-24'>
      <AnimateInView
        className='mx-auto flex max-w-6xl flex-col justify-between gap-8 md:flex-row md:items-end'
        animation='fade-up'
      >
        <div className='max-w-2xl'>
          <p className='text-primary text-xs font-semibold'>
            {t('home.cta.eyebrow')}
          </p>
          <h2 className='mt-2 text-2xl font-bold md:text-3xl'>
            {t('home.cta.title')}
          </h2>
          <p className='text-muted-foreground mt-3 max-w-xl text-sm leading-relaxed md:text-base'>
            {t('home.cta.copy')}
          </p>
        </div>

        <div className='flex shrink-0 flex-wrap gap-3'>
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
        </div>
      </AnimateInView>
    </section>
  )
}
