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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { CapabilityMarksPanel } from './components/capability-marks-panel'
import { PolicyPanel } from './components/policy-panel'

/**
 * Administration page for the official-fit pin layer.
 *
 * The two halves answer the two questions that have no other answer in the
 * product: which behaviours the fleet claims to reproduce, and which rule
 * document decides what a request needs. Both were previously only visible by
 * querying the database and the option table by hand.
 */
export function FitPolicy() {
  const { t } = useTranslation()
  const [tab, setTab] = useState('marks')

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Fit Capability')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value='marks'>{t('Capability marks')}</TabsTrigger>
            <TabsTrigger value='policy'>{t('Policy')}</TabsTrigger>
          </TabsList>
          <TabsContent value='marks' className='mt-4'>
            <CapabilityMarksPanel />
          </TabsContent>
          <TabsContent value='policy' className='mt-4'>
            <PolicyPanel />
          </TabsContent>
        </Tabs>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
