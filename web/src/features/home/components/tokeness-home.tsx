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
  AzureAI,
  Claude,
  Cohere,
  DeepSeek,
  Grok,
  Hunyuan,
  Midjourney,
  Minimax,
  Moonshot,
  Qingyan,
  Spark,
  Suno,
  Volcengine,
  Wenxin,
  XAI,
  Xinference,
  Zhipu,
} from '@lobehub/icons'
import { Link } from '@tanstack/react-router'
import type { ComponentType, SVGProps } from 'react'
import { useTranslation } from 'react-i18next'

import {
  LegacyGeminiIcon,
  LegacyOpenAIIcon,
  LegacyQwenIcon,
} from './legacy-provider-icons'

import './tokeness-home.css'

type ProviderIcon = ComponentType<SVGProps<SVGSVGElement>>

interface Provider {
  id: string
  icon: ProviderIcon
  name: string
}

const PROVIDERS: Provider[] = [
  { id: 'moonshot', name: 'MoonshotAI', icon: Moonshot },
  { id: 'openai', name: 'OpenAI', icon: LegacyOpenAIIcon },
  { id: 'xai', name: 'Grok', icon: XAI },
  { id: 'zhipu', name: 'Zhipu', icon: Zhipu.Color },
  { id: 'volcengine', name: 'Volcengine', icon: Volcengine.Color },
  { id: 'cohere', name: 'Cohere', icon: Cohere.Color },
  { id: 'claude', name: 'Claude', icon: Claude.Color },
  { id: 'gemini', name: 'Gemini', icon: LegacyGeminiIcon },
  { id: 'suno', name: 'Suno', icon: Suno },
  { id: 'minimax', name: 'Minimax', icon: Minimax.Color },
  { id: 'wenxin', name: 'Wenxin', icon: Wenxin.Color },
  { id: 'spark', name: 'Spark', icon: Spark.Color },
  { id: 'qingyan', name: 'Qingyan', icon: Qingyan.Color },
  { id: 'deepseek', name: 'DeepSeek', icon: DeepSeek.Color },
  { id: 'qwen', name: 'Qwen', icon: LegacyQwenIcon },
  { id: 'midjourney', name: 'Midjourney', icon: Midjourney },
  { id: 'grok', name: 'Grok', icon: Grok },
  { id: 'azure-ai', name: 'AzureAI', icon: AzureAI.Color },
  { id: 'hunyuan', name: 'Hunyuan', icon: Hunyuan.Color },
  { id: 'xinference', name: 'Xinference', icon: Xinference.Color },
]

const HOME_KEYS = {
  dashboard: 'home.legacy.actions.dashboard',
  pricing: 'home.legacy.actions.pricing',
  stepsTitle: 'home.legacy.steps.title',
  steps: [
    'home.legacy.steps.01',
    'home.legacy.steps.02',
    'home.legacy.steps.03',
  ],
  supplier: 'home.legacy.supplier',
  routingTitle: 'home.legacy.routing.title',
  routingItems: [
    'home.legacy.routing.01',
    'home.legacy.routing.02',
    'home.legacy.routing.03',
  ],
  compatibilityTitle: 'home.legacy.compatibility.title',
  routingSpec: 'home.legacy.compatibility.routing',
  outputSpec: 'home.legacy.compatibility.output',
  auditSpec: 'home.legacy.compatibility.audit',
  footerCopy: 'home.legacy.footer.copy',
  footerModels: 'home.legacy.footer.models',
  footerContact: 'home.legacy.footer.contact',
  footerPoweredBy: 'home.legacy.footer.poweredBy',
  footerPrivacy: 'home.legacy.footer.privacy',
  footerTerms: 'home.legacy.footer.terms',
} as const

export function TokenessHome() {
  const { t } = useTranslation()
  const capabilities = [
    {
      index: 'A01',
      title: t('home.legacy.capabilities.key.title'),
      copy: t('home.legacy.capabilities.key.copy'),
    },
    {
      index: 'A02',
      title: t('home.legacy.capabilities.quota.title'),
      copy: t('home.legacy.capabilities.quota.copy'),
    },
    {
      index: 'A03',
      title: t('home.legacy.capabilities.relay.title'),
      copy: t('home.legacy.capabilities.relay.copy'),
    },
    {
      index: 'A04',
      title: t('home.legacy.capabilities.audit.title'),
      copy: t('home.legacy.capabilities.audit.copy'),
    },
  ]

  return (
    <main className='tokeness-home' data-testid='tokeness-home'>
      <div className='tokeness-home__shell'>
        <div className='tokeness-home__top-spacer' aria-hidden='true' />

        <section className='tokeness-home__hero'>
          <div className='tokeness-home__hero-main'>
            <div className='tokeness-home__system-mark'>
              {t('home.legacy.hero.kicker')}
            </div>
            <h1 className='tokeness-home__hero-title'>
              {t('home.legacy.hero.title')}
            </h1>
            <p className='tokeness-home__hero-copy'>
              {t('home.legacy.hero.copy')}
            </p>
            <div className='tokeness-home__actions'>
              <Link
                to='/dashboard'
                className='tokeness-home__button tokeness-home__button--primary'
              >
                {t(HOME_KEYS.dashboard)}
              </Link>
              <Link
                to='/pricing'
                className='tokeness-home__button tokeness-home__button--secondary'
              >
                {t(HOME_KEYS.pricing)}
              </Link>
            </div>
          </div>

          <aside className='tokeness-home__hero-side'>
            <h2 className='tokeness-home__side-title'>
              {t(HOME_KEYS.stepsTitle)}
            </h2>
            <ol className='tokeness-home__steps'>
              {HOME_KEYS.steps.map((key, index) => (
                <li key={key} className='tokeness-home__step'>
                  <span className='tokeness-home__step-number'>
                    {index + 1}
                  </span>
                  <span>{t(key)}</span>
                </li>
              ))}
            </ol>
          </aside>
        </section>

        <section
          className='tokeness-home__capabilities'
          aria-label={t('home.legacy.capabilities.label')}
        >
          {capabilities.map((capability) => (
            <article
              key={capability.index}
              className='tokeness-home__capability'
              data-index={capability.index}
            >
              <strong>{capability.title}</strong>
              <span>{capability.copy}</span>
            </article>
          ))}
        </section>

        <section className='tokeness-home__band'>
          <div className='tokeness-home__band-text'>
            <div className='tokeness-home__band-label'>
              {t('home.legacy.system.label')}
            </div>
            <h2 className='tokeness-home__band-title'>
              {t('home.legacy.system.title')}
            </h2>
            <p className='tokeness-home__band-copy'>
              {t('home.legacy.system.copy')}
            </p>
          </div>
          <div className='tokeness-home__band-stats'>
            <div className='tokeness-home__stat'>
              <b>30+</b>
              <span>providers</span>
            </div>
            <div className='tokeness-home__stat'>
              <b>1</b>
              <span>gateway</span>
            </div>
            <div className='tokeness-home__stat'>
              <b>3</b>
              <span>layers</span>
            </div>
          </div>
        </section>

        <section className='tokeness-home__block tokeness-home__supplier'>
          <h2 className='tokeness-home__supplier-label'>
            {t(HOME_KEYS.supplier)}
          </h2>
          <div
            className='tokeness-home__provider-matrix'
            aria-label={t(HOME_KEYS.supplier)}
          >
            {PROVIDERS.map((provider) => {
              const Icon = provider.icon
              return (
                <div
                  key={provider.id}
                  className='tokeness-home__provider-tile'
                  title={provider.name}
                >
                  <Icon aria-hidden='true' focusable='false' />
                  <span className='sr-only'>{provider.name}</span>
                </div>
              )
            })}
            <div className='tokeness-home__provider-tile tokeness-home__provider-more'>
              30+
            </div>
          </div>
        </section>

        <section className='tokeness-home__footer-grid'>
          <article className='tokeness-home__block'>
            <h2 className='tokeness-home__block-title'>
              {t(HOME_KEYS.routingTitle)}
            </h2>
            <ol className='tokeness-home__list'>
              {HOME_KEYS.routingItems.map((key, index) => (
                <li key={key} className='tokeness-home__list-item'>
                  <i>{String(index + 1).padStart(2, '0')}</i>
                  <span>{t(key)}</span>
                </li>
              ))}
            </ol>
          </article>

          <article className='tokeness-home__block'>
            <h2 className='tokeness-home__block-title'>
              {t(HOME_KEYS.compatibilityTitle)}
            </h2>
            <dl className='tokeness-home__spec-table'>
              <div className='tokeness-home__spec-row'>
                <dt>SDK</dt>
                <dd>OpenAI / Claude / Gemini / Qwen</dd>
              </div>
              <div className='tokeness-home__spec-row'>
                <dt>Routing</dt>
                <dd>{t(HOME_KEYS.routingSpec)}</dd>
              </div>
              <div className='tokeness-home__spec-row'>
                <dt>Output</dt>
                <dd>{t(HOME_KEYS.outputSpec)}</dd>
              </div>
              <div className='tokeness-home__spec-row'>
                <dt>Audit</dt>
                <dd>{t(HOME_KEYS.auditSpec)}</dd>
              </div>
            </dl>
          </article>
        </section>

        <footer className='tokeness-home__site-footer'>
          <div className='tokeness-home__site-footer-main'>
            <div className='tokeness-home__footer-brand'>Tokeness</div>
            <p className='tokeness-home__footer-copy'>
              {t(HOME_KEYS.footerCopy)}
            </p>
            <div className='tokeness-home__copyright'>
              <span>© 2026 Tokeness. All rights reserved.</span>
              <a
                href='https://github.com/QuantumNous/new-api'
                target='_blank'
                rel='noopener noreferrer'
                className='tokeness-home__footer-link'
              >
                {t(HOME_KEYS.footerPoweredBy)}
              </a>
            </div>
            <nav
              className='tokeness-home__footer-nav'
              aria-label='Tokeness footer navigation'
            >
              <Link to='/pricing' className='tokeness-home__footer-link'>
                {t(HOME_KEYS.footerModels)}
              </Link>
              <a
                href='mailto:contact@tokeness.io'
                className='tokeness-home__footer-link'
              >
                {t(HOME_KEYS.footerContact)}
              </a>
            </nav>
          </div>

          <div className='tokeness-home__footer-right'>
            <a
              className='tokeness-home__footer-badge'
              href='https://lmspeed.net/provider/tokeness-cn'
              target='_blank'
              rel='noopener noreferrer'
            >
              <img
                src='https://lmspeed.net/api/provider/claim-badge/1278?claim=1278--EDVRn1QWCO5Q_Feyad9cpuqsjUZVIb3'
                alt='Verified on LM Speed'
              />
            </a>
            <nav
              className='tokeness-home__footer-legal'
              aria-label='Legal links'
            >
              <Link to='/privacy-policy' className='tokeness-home__footer-link'>
                {t(HOME_KEYS.footerPrivacy)}
              </Link>
              <Link to='/user-agreement' className='tokeness-home__footer-link'>
                {t(HOME_KEYS.footerTerms)}
              </Link>
            </nav>
          </div>
        </footer>
      </div>
    </main>
  )
}
