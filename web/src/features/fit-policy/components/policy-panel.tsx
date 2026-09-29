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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  CircleCheck,
  CircleX,
  RotateCcw,
  PowerOff,
  ShieldAlert,
} from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  getFitPolicy,
  saveFitPolicyDocument,
  validateFitPolicyDocument,
} from '../api'
import {
  FIT_POLICY_SOURCE_LABEL_KEYS,
  FIT_POLICY_WARNING_LEVELS,
  FIT_POLICY_WARNING_MESSAGE_KEYS,
  evaluatePolicySave,
  policySaveImpact,
  truncateFitHash,
} from '../lib/policy-format'
import type { FitPolicyValidation, FitPolicyWarning } from '../types'

/**
 * The policy document editor.
 *
 * Three rules shape this component, and each of them exists because the layer
 * can be switched off without anything failing loudly:
 *
 *  1. the document is validated by the backend before the save button unlocks,
 *     and editing the text re-arms that check;
 *  2. a document that would leave the layer pinning nothing is confirmed
 *     explicitly instead of saved quietly;
 *  3. the difference from the shipped default, the warnings and the live
 *     snapshot are always on screen, so "it saved" never has to be read as
 *     "it works".
 */
export function PolicyPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const isRoot = useAuthStore(
    (state) => state.auth.user?.role === ROLE.SUPER_ADMIN
  )

  const policyQuery = useQuery({
    queryKey: ['fit-policy'],
    queryFn: getFitPolicy,
  })

  const [document, setDocument] = useState('')
  const [baselineDocument, setBaselineDocument] = useState('')
  const [validation, setValidation] = useState<FitPolicyValidation | null>(null)
  const [validatedDocument, setValidatedDocument] = useState<string | null>(
    null
  )
  const [confirmDisableOpen, setConfirmDisableOpen] = useState(false)

  // Load the editor from the server, and reload it after a save. The document
  // shown is what is stored; when the option has never been written the shipped
  // default is loaded so the rules in force are visible, while the unchanged
  // check still compares against the (empty) stored value.
  const storedDocument = policyQuery.data?.document ?? ''
  const defaultDocument = policyQuery.data?.default_document ?? ''
  useEffect(() => {
    if (!policyQuery.data) return
    let initial = storedDocument
    if (policyQuery.data.source === 'default') {
      initial = defaultDocument
    } else if (storedDocument.trim() === '') {
      initial = ''
    }
    setDocument(initial)
    setBaselineDocument(storedDocument)
    setValidation(null)
    setValidatedDocument(null)
  }, [policyQuery.data, defaultDocument, storedDocument])

  const validateMutation = useMutation({
    mutationFn: validateFitPolicyDocument,
    onSuccess: (result, submitted) => {
      setValidation(result)
      setValidatedDocument(submitted)
      if (!result.valid) {
        toast.error(t('Validation error'), { description: result.error })
      }
    },
    onError: (error) => handleServerError(error, t('Validation error')),
  })

  const saveMutation = useMutation({
    mutationFn: saveFitPolicyDocument,
    onSuccess: async () => {
      toast.success(t('Saved'))
      await queryClient.invalidateQueries({ queryKey: ['fit-policy'] })
      await queryClient.invalidateQueries({
        queryKey: ['fit-capability-marks'],
      })
    },
    onError: (error) => handleServerError(error, t('Failed to save')),
  })

  const gate = evaluatePolicySave({
    document,
    baselineDocument,
    validatedDocument,
    validation,
    isRoot,
  })
  const impact = useMemo(() => policySaveImpact(validation), [validation])

  if (policyQuery.isError) {
    return (
      <ErrorState
        title={t('Failed to load')}
        description={
          policyQuery.error instanceof Error
            ? policyQuery.error.message
            : undefined
        }
        onRetry={() => policyQuery.refetch()}
      />
    )
  }
  if (policyQuery.isLoading || !policyQuery.data) {
    return <Skeleton className='h-96 w-full' />
  }

  const view = policyQuery.data

  const submitSave = () => {
    if (!gate.allowed) return
    if (impact.disablesLayer) {
      setConfirmDisableOpen(true)
      return
    }
    saveMutation.mutate(document)
  }

  const loadDocument = (next: string, validate: boolean) => {
    setDocument(next)
    setValidation(null)
    setValidatedDocument(null)
    if (validate) validateMutation.mutate(next)
  }

  return (
    <div className='space-y-4'>
      <Card>
        <CardHeader>
          <CardTitle>{t('Fit policy document')}</CardTitle>
        </CardHeader>
        <CardContent className='space-y-4'>
          <div className='flex flex-wrap items-center gap-2'>
            <span className='text-muted-foreground text-sm'>
              {t('In force')}:
            </span>
            <Badge
              variant={view.source === 'document' ? 'default' : 'secondary'}
            >
              {t(FIT_POLICY_SOURCE_LABEL_KEYS[view.source])}
            </Badge>
            <span className='text-muted-foreground text-sm'>
              {t('Live policy')}:
            </span>
            {view.live.installed ? (
              <>
                <Badge variant={view.live.enabled ? 'default' : 'destructive'}>
                  {view.live.enabled ? t('Enabled') : t('Disabled')}
                </Badge>
                <Badge variant={view.live.shadow ? 'warning' : 'outline'}>
                  {view.live.shadow ? t('Shadow') : t('Enforced')}
                </Badge>
                <span className='font-mono text-xs'>
                  v{view.live.version} · {truncateFitHash(view.live.hash)}
                </span>
              </>
            ) : (
              <Badge variant='secondary'>{t('No policy installed')}</Badge>
            )}
          </div>

          {view.live.baseline ? (
            <p className='text-muted-foreground text-xs'>
              {t('Baseline')}:{' '}
              <span className='font-mono'>{view.live.baseline}</span>
            </p>
          ) : null}

          {view.parse_error ? (
            <Alert variant='destructive'>
              <CircleX />
              <AlertTitle>
                {t('The stored document no longer compiles.')}
              </AlertTitle>
              <AlertDescription className='break-all'>
                {view.parse_error}
              </AlertDescription>
            </Alert>
          ) : null}

          {view.live.last_error ? (
            <Alert variant='destructive'>
              <CircleX />
              <AlertTitle>
                {t('The running policy kept the last working document.')}
              </AlertTitle>
              <AlertDescription className='break-all'>
                {view.live.last_error}
              </AlertDescription>
            </Alert>
          ) : null}

          <WarningList warnings={view.warnings} />

          <div className='space-y-1'>
            <p className='text-sm font-medium'>
              {t('Difference from the shipped default')}
            </p>
            {view.divergence ? (
              <ul className='text-muted-foreground list-disc space-y-0.5 ps-5 text-xs'>
                {view.divergence.split('; ').map((part) => (
                  <li key={part} className='font-mono'>
                    {part}
                  </li>
                ))}
              </ul>
            ) : (
              <p className='text-muted-foreground text-xs'>
                {t('No difference from the shipped default.')}
              </p>
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
          <CardTitle>{t('Edit the document')}</CardTitle>
          <div className='flex flex-wrap gap-2'>
            <Button
              variant='outline'
              size='sm'
              disabled={!isRoot || saveMutation.isPending}
              onClick={() => loadDocument(defaultDocument, true)}
            >
              <RotateCcw />
              {t('Restore the shipped default')}
            </Button>
            <Button
              variant='outline'
              size='sm'
              disabled={!isRoot || saveMutation.isPending}
              onClick={() => loadDocument('', true)}
            >
              <PowerOff />
              {t('Disable the layer')}
            </Button>
          </div>
        </CardHeader>
        <CardContent className='space-y-3'>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Both buttons only load text into the editor. Nothing is written until Validate has accepted the exact text and Save is pressed.'
            )}
          </p>
          <JsonCodeEditor
            value={document}
            onChange={setDocument}
            disabled={!isRoot || saveMutation.isPending}
            heightClassName='h-80 min-h-80 max-h-80'
            ariaLabel={t('Fit policy document')}
          />

          <div className='flex flex-wrap items-center gap-2'>
            <Button
              variant='secondary'
              size='sm'
              disabled={validateMutation.isPending}
              onClick={() => validateMutation.mutate(document)}
            >
              {validateMutation.isPending ? t('Validating') : t('Validate')}
            </Button>
            <Button
              size='sm'
              disabled={!gate.allowed || saveMutation.isPending}
              onClick={submitSave}
            >
              {saveMutation.isPending ? t('Saving') : t('Save')}
            </Button>
            {!gate.allowed && gate.reasonKey ? (
              <span className='text-muted-foreground text-xs'>
                {t(gate.reasonKey)}
              </span>
            ) : null}
          </div>

          {validation ? (
            <div className='space-y-2 rounded-md border p-3'>
              <div className='flex items-center gap-2 text-sm'>
                {validation.valid ? (
                  <CircleCheck className='text-primary size-4' />
                ) : (
                  <CircleX className='text-destructive size-4' />
                )}
                {validation.valid
                  ? t('Validation passed')
                  : t('Validation error')}
              </div>
              {validation.error ? (
                <p className='text-destructive text-xs break-all'>
                  {validation.error}
                </p>
              ) : null}
              {validation.summary ? (
                <p className='text-muted-foreground text-xs'>
                  v{validation.summary.version} ·{' '}
                  {validation.summary.enabled ? t('Enabled') : t('Disabled')} ·{' '}
                  {validation.summary.shadow ? t('Shadow') : t('Enforced')} ·{' '}
                  {validation.summary.families.length} {t('Family')} ·{' '}
                  {validation.summary.rules} {t('Rules')} ·{' '}
                  <span className='font-mono'>
                    {truncateFitHash(validation.summary.hash)}
                  </span>
                </p>
              ) : null}
              <WarningList warnings={validation.warnings} />
            </div>
          ) : (
            <p className='text-muted-foreground text-xs'>
              {t('Validate the document before saving it.')}
            </p>
          )}
        </CardContent>
      </Card>

      <ConfirmDialog
        open={confirmDisableOpen}
        onOpenChange={setConfirmDisableOpen}
        title={t('Switch the official-fit policy layer off?')}
        desc={t(
          'Saving this document switches the official-fit policy layer off: no request is pinned to a channel that reproduces the official behaviour.'
        )}
        confirmText={t('Save anyway')}
        destructive
        isLoading={saveMutation.isPending}
        handleConfirm={() => {
          setConfirmDisableOpen(false)
          saveMutation.mutate(document)
        }}
      />
    </div>
  )
}

const WARNING_ICONS = {
  critical: ShieldAlert,
  warning: ShieldAlert,
  info: CircleCheck,
} as const

function WarningList({ warnings }: { warnings: FitPolicyWarning[] }) {
  const { t } = useTranslation()
  if (warnings.length === 0) return null
  const critical = warnings.filter(
    (warning) => FIT_POLICY_WARNING_LEVELS[warning.code] === 'critical'
  )
  if (critical.length > 0) {
    return (
      <Alert variant='destructive'>
        <ShieldAlert />
        <AlertTitle>{t('Fit policy warnings')}</AlertTitle>
        <AlertDescription>
          <ul className='list-disc space-y-0.5 ps-4'>
            {warnings.map((warning) => (
              <li key={warning.code}>
                {t(FIT_POLICY_WARNING_MESSAGE_KEYS[warning.code])}
              </li>
            ))}
          </ul>
        </AlertDescription>
      </Alert>
    )
  }
  return (
    <div className='text-muted-foreground space-y-1 text-xs'>
      {warnings.map((warning) => {
        const Icon = WARNING_ICONS[FIT_POLICY_WARNING_LEVELS[warning.code]]
        return (
          <div key={warning.code} className='flex items-center gap-1.5'>
            <Icon className='size-3.5' />
            {t(FIT_POLICY_WARNING_MESSAGE_KEYS[warning.code])}
          </div>
        )
      })}
    </div>
  )
}
