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
import {
  Activity,
  ArrowRight,
  BadgeDollarSign,
  KeyRound,
  Route,
  ShieldCheck,
  Waypoints,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'

interface FeaturesProps {
  className?: string
}

export function Features(_props: FeaturesProps) {
  const { t } = useTranslation()
  const controls = [
    {
      id: 'key',
      icon: <KeyRound className='size-5' strokeWidth={1.5} />,
      title: t('home.controls.key.title'),
      copy: t('home.controls.key.copy'),
    },
    {
      id: 'quota',
      icon: <BadgeDollarSign className='size-5' strokeWidth={1.5} />,
      title: t('home.controls.quota.title'),
      copy: t('home.controls.quota.copy'),
    },
    {
      id: 'routing',
      icon: <Route className='size-5' strokeWidth={1.5} />,
      title: t('home.controls.routing.title'),
      copy: t('home.controls.routing.copy'),
    },
    {
      id: 'usage',
      icon: <Activity className='size-5' strokeWidth={1.5} />,
      title: t('home.controls.usage.title'),
      copy: t('home.controls.usage.copy'),
    },
  ]
  const requestPath = [
    t('home.trace.key'),
    t('home.trace.quota'),
    t('home.trace.route'),
    t('home.trace.relay'),
    t('home.trace.response'),
  ]

  return (
    <section className='relative z-10 px-6 py-20 md:py-24'>
      <div className='mx-auto max-w-6xl'>
        <AnimateInView className='max-w-2xl'>
          <p className='text-primary text-xs font-semibold'>
            {t('home.controls.eyebrow')}
          </p>
          <h2 className='mt-2 text-2xl font-bold md:text-3xl'>
            {t('home.controls.title')}
          </h2>
          <p className='text-muted-foreground mt-3 max-w-xl text-sm leading-relaxed md:text-base'>
            {t('home.controls.copy')}
          </p>
        </AnimateInView>

        <div className='mt-10 grid gap-4 sm:grid-cols-2 lg:grid-cols-4'>
          {controls.map((control, index) => (
            <AnimateInView
              key={control.id}
              animation='fade-up'
              delay={index * 80}
              className='border-border/60 bg-background rounded-lg border p-5'
            >
              <div className='bg-muted text-foreground flex size-9 items-center justify-center rounded-md'>
                {control.icon}
              </div>
              <h3 className='mt-5 text-base font-semibold'>{control.title}</h3>
              <p className='text-muted-foreground mt-2 text-sm leading-relaxed'>
                {control.copy}
              </p>
            </AnimateInView>
          ))}
        </div>

        <AnimateInView
          className='border-border/60 mt-16 border-y py-10 md:mt-20 md:py-12'
          animation='fade-in'
        >
          <div className='grid gap-10 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.25fr)] lg:items-center'>
            <div>
              <p className='text-primary text-xs font-semibold'>
                {t('home.trace.eyebrow')}
              </p>
              <h2 className='mt-2 text-2xl font-bold md:text-3xl'>
                {t('home.trace.title')}
              </h2>
              <p className='text-muted-foreground mt-3 max-w-xl text-sm leading-relaxed md:text-base'>
                {t('home.trace.copy')}
              </p>
            </div>

            <div className='space-y-5'>
              <ol
                className='flex flex-wrap items-center gap-2'
                aria-label={t('home.trace.pathLabel')}
              >
                {requestPath.map((step, index) => (
                  <li key={step} className='flex items-center gap-2'>
                    <span className='border-border bg-muted/40 rounded-md border px-2.5 py-1.5 font-mono text-xs'>
                      {step}
                    </span>
                    {index < requestPath.length - 1 ? (
                      <ArrowRight
                        className='text-muted-foreground size-3.5'
                        aria-hidden='true'
                      />
                    ) : null}
                  </li>
                ))}
              </ol>

              <div className='grid gap-3 sm:grid-cols-2'>
                <div className='flex items-start gap-3'>
                  <ShieldCheck
                    className='text-primary mt-0.5 size-5 shrink-0'
                    strokeWidth={1.5}
                    aria-hidden='true'
                  />
                  <div>
                    <h3 className='text-sm font-semibold'>
                      {t('home.trace.audit.title')}
                    </h3>
                    <p className='text-muted-foreground mt-1 text-xs leading-relaxed'>
                      {t('home.trace.audit.copy')}
                    </p>
                  </div>
                </div>
                <div className='flex items-start gap-3'>
                  <Waypoints
                    className='text-primary mt-0.5 size-5 shrink-0'
                    strokeWidth={1.5}
                    aria-hidden='true'
                  />
                  <div>
                    <h3 className='text-sm font-semibold'>
                      {t('home.trace.compatibility.title')}
                    </h3>
                    <p className='text-muted-foreground mt-1 text-xs leading-relaxed'>
                      {t('home.trace.compatibility.copy')}
                    </p>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </AnimateInView>
      </div>
    </section>
  )
}
