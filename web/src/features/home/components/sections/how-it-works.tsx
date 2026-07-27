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
import { Code2, KeyRound, Route } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'

export function HowItWorks() {
  const { t } = useTranslation()
  const steps = [
    {
      number: '01',
      title: t('home.how.key.title'),
      copy: t('home.how.key.copy'),
      icon: <KeyRound className='size-5' strokeWidth={1.5} />,
    },
    {
      number: '02',
      title: t('home.how.route.title'),
      copy: t('home.how.route.copy'),
      icon: <Route className='size-5' strokeWidth={1.5} />,
    },
    {
      number: '03',
      title: t('home.how.connect.title'),
      copy: t('home.how.connect.copy'),
      icon: <Code2 className='size-5' strokeWidth={1.5} />,
    },
  ]

  return (
    <section className='border-border/40 bg-muted/15 relative z-10 border-y px-6 py-20 md:py-24'>
      <div className='mx-auto max-w-6xl'>
        <AnimateInView className='max-w-2xl'>
          <p className='text-primary text-xs font-semibold'>
            {t('home.how.eyebrow')}
          </p>
          <h2 className='mt-2 text-2xl font-bold md:text-3xl'>
            {t('home.how.title')}
          </h2>
        </AnimateInView>

        <ol className='mt-10 grid gap-px overflow-hidden rounded-lg border md:grid-cols-3'>
          {steps.map((step, index) => (
            <AnimateInView
              key={step.number}
              as='li'
              animation='fade-up'
              delay={index * 100}
              className='bg-background min-h-56 p-6 md:p-7'
            >
              <div className='flex items-center justify-between'>
                <span className='bg-muted flex size-9 items-center justify-center rounded-md'>
                  {step.icon}
                </span>
                <span className='text-muted-foreground font-mono text-xs'>
                  {step.number}
                </span>
              </div>
              <h3 className='mt-10 text-lg font-semibold'>{step.title}</h3>
              <p className='text-muted-foreground mt-3 text-sm leading-relaxed'>
                {step.copy}
              </p>
            </AnimateInView>
          ))}
        </ol>
      </div>
    </section>
  )
}
