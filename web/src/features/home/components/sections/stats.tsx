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
import { Claude, DeepSeek, Gemini, OpenAI, Qwen } from '@lobehub/icons'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'

interface StatsProps {
  className?: string
}

const PROVIDERS = [
  { name: 'OpenAI', icon: <OpenAI size={26} /> },
  { name: 'Claude', icon: <Claude.Color size={26} /> },
  { name: 'Gemini', icon: <Gemini.Color size={26} /> },
  { name: 'DeepSeek', icon: <DeepSeek.Color size={26} /> },
  { name: 'Qwen', icon: <Qwen.Color size={26} /> },
] as const

export function Stats(_props: StatsProps) {
  const { t } = useTranslation()

  return (
    <section className='border-border/40 bg-muted/15 relative z-10 border-b px-6 py-12 md:py-14'>
      <div className='mx-auto grid max-w-6xl gap-8 md:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)] md:items-center'>
        <AnimateInView>
          <p className='text-primary text-xs font-semibold'>
            {t('home.providers.eyebrow')}
          </p>
          <h2 className='mt-2 text-xl font-semibold md:text-2xl'>
            {t('home.providers.title')}
          </h2>
          <p className='text-muted-foreground mt-2 max-w-md text-sm leading-relaxed'>
            {t('home.providers.copy')}
          </p>
        </AnimateInView>

        <AnimateInView
          animation='fade-in'
          className='grid grid-cols-3 gap-px overflow-hidden rounded-lg border md:grid-cols-6'
          delay={100}
        >
          {PROVIDERS.map((provider) => (
            <div
              key={provider.name}
              className='bg-background flex min-h-20 flex-col items-center justify-center gap-2 px-2 py-4'
            >
              <span aria-hidden='true'>{provider.icon}</span>
              <span className='text-muted-foreground text-xs font-medium'>
                {provider.name}
              </span>
            </div>
          ))}
          <div className='bg-background flex min-h-20 flex-col items-center justify-center gap-1 px-2 py-4'>
            <strong className='text-xl tabular-nums'>30+</strong>
            <span className='text-muted-foreground text-xs'>
              {t('home.providers.more')}
            </span>
          </div>
        </AnimateInView>
      </div>
    </section>
  )
}
