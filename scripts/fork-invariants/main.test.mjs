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
/**
 * Tests for the fork invariants gate itself.
 *
 * The interesting case is the one that cost us twice: a merge that resolves a
 * conflict by taking upstream's file wholesale, dropping a fork change while
 * every test suite stays green. The synthetic repository below reproduces it
 * and asserts the gate fails.
 *
 * Run: node --test scripts/fork-invariants/main.test.mjs
 */
import assert from 'node:assert/strict'
import { execFileSync, spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { after, before, describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const GATE = path.join(HERE, 'main.mjs')

function git(cwd, args) {
  return execFileSync('git', args, {
    cwd,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: 'test',
      GIT_AUTHOR_EMAIL: 'test@example.com',
      GIT_COMMITTER_NAME: 'test',
      GIT_COMMITTER_EMAIL: 'test@example.com',
      GIT_CONFIG_GLOBAL: '/dev/null',
      GIT_CONFIG_SYSTEM: '/dev/null',
    },
  })
}

function gitAllowFailure(cwd, args) {
  try {
    return git(cwd, args)
  } catch {
    return ''
  }
}

function runGate(repo, args) {
  const result = spawnSync(process.execPath, [GATE, '--repo', repo, ...args], {
    encoding: 'utf8',
  })
  return { code: result.status, stdout: result.stdout ?? '', stderr: result.stderr ?? '' }
}

let tmp

/** Repository with a fork branch and an upstream branch that both touch one file. */
function seedRepo({ takeUpstreamWholesale }) {
  const repo = fs.mkdtempSync(path.join(tmp, 'repo-'))
  git(repo, ['init', '-q', '-b', 'main'])
  fs.mkdirSync(path.join(repo, 'service'), { recursive: true })
  fs.writeFileSync(
    path.join(repo, 'service', 'video.go'),
    ['package service', '', 'func shared() string {', '\treturn "shared"', '}', ''].join('\n')
  )
  git(repo, ['add', '.'])
  git(repo, ['commit', '-qm', 'shared baseline'])

  // The fork adds its hardening; upstream branches off the same parent and
  // rewrites the file, so the two sides conflict on the merge.
  git(repo, ['branch', 'upstream/main'])
  fs.writeFileSync(
    path.join(repo, 'service', 'video.go'),
    [
      'package service',
      '',
      '// forkOnlyHardening keeps the fork behaviour.',
      'func forkOnlyHardening() string {',
      '\treturn "fork"',
      '}',
      '',
      'func shared() string {',
      '\treturn "shared"',
      '}',
      '',
    ].join('\n')
  )
  git(repo, ['commit', '-qam', 'fork: add hardening'])
  const forkTip = git(repo, ['rev-parse', 'HEAD']).trim()

  git(repo, ['checkout', '-q', 'upstream/main'])
  fs.writeFileSync(
    path.join(repo, 'service', 'video.go'),
    [
      'package service',
      '',
      'func shared() string {',
      '\treturn "shared (upstream)"',
      '}',
      '',
    ].join('\n')
  )
  git(repo, ['commit', '-qam', 'upstream: rewrite video handling'])
  git(repo, ['checkout', '-q', 'main'])

  // A real sync merge: `git merge` stops on the conflict and the resolution is
  // written into the merge commit itself, exactly like the rcNN syncs.
  git(repo, ['checkout', '-q', '-b', 'sync', forkTip])
  gitAllowFailure(repo, ['merge', '--no-commit', '--no-ff', 'upstream/main'])
  fs.writeFileSync(
    path.join(repo, 'service', 'video.go'),
    takeUpstreamWholesale
      ? ['package service', '', 'func shared() string {', '\treturn "shared (upstream)"', '}', ''].join('\n')
      : [
          'package service',
          '',
          '// forkOnlyHardening keeps the fork behaviour.',
          'func forkOnlyHardening() string {',
          '\treturn "fork"',
          '}',
          '',
          'func shared() string {',
          '\treturn "shared (upstream)"',
          '}',
          '',
        ].join('\n')
  )
  git(repo, ['add', 'service/video.go'])
  git(repo, ['commit', '-qm', 'merge: sync upstream'])
  return repo
}

before(() => {
  tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'fork-invariants-'))
})

after(() => {
  fs.rmSync(tmp, { recursive: true, force: true })
})

describe('merge divergence check', () => {
  test('passes when the merge keeps both sides', () => {
    const repo = seedRepo({ takeUpstreamWholesale: false })
    git(repo, ['checkout', '-q', 'sync'])
    const { code, stdout } = runGate(repo, ['--check', 'merge', '--merge', 'HEAD'])
    assert.equal(code, 0, stdout)
    assert.match(stdout, /PASS {2}merge did not drop fork work/)
  })

  test('fails when the merge takes upstream whole and loses fork work', () => {
    const repo = seedRepo({ takeUpstreamWholesale: true })
    git(repo, ['checkout', '-q', 'sync'])
    const { code, stdout } = runGate(repo, ['--check', 'merge', '--merge', 'HEAD'])
    assert.equal(code, 1, stdout)
    assert.match(stdout, /service\/video\.go \(\+\d+ fork lines lost\)/)
  })

  test('an allowlisted path stops failing but stays visible', () => {
    const repo = seedRepo({ takeUpstreamWholesale: true })
    git(repo, ['checkout', '-q', 'sync'])
    const allowlist = path.join(tmp, 'allow.json')
    fs.writeFileSync(
      allowlist,
      JSON.stringify({ paths: [{ path: 'service/video.go', reason: 'moved to video_hardening.go' }] })
    )
    const { code, stdout } = runGate(repo, [
      '--check',
      'merge',
      '--merge',
      'HEAD',
      '--merge-allowlist',
      allowlist,
    ])
    assert.equal(code, 0, stdout)
    assert.match(stdout, /acknowledged by merge-allowlist\.json: 1/)
  })
})

describe('manifest survival check', () => {
  function manifestRepo() {
    const repo = seedRepo({ takeUpstreamWholesale: false })
    const manifest = path.join(tmp, `manifest-${Math.random().toString(36).slice(2)}.json`)
    fs.writeFileSync(
      manifest,
      JSON.stringify({
        entries: [
          {
            id: 'video-hardening',
            title: 'fork video hardening',
            files: ['service/video.go'],
            anchors: [{ kind: 'symbol', file: 'service/video.go', name: 'forkOnlyHardening' }],
          },
        ],
      })
    )
    return { repo, manifest }
  }

  test('passes while the fork symbol is present', () => {
    const { repo, manifest } = manifestRepo()
    const { code } = runGate(repo, ['--check', 'manifest', '--manifest', manifest])
    assert.equal(code, 0)
  })

  test('fails when an upstream sync drops the fork symbol', () => {
    const { repo, manifest } = manifestRepo()
    fs.writeFileSync(path.join(repo, 'service', 'video.go'), 'package service\n')
    const { code, stdout } = runGate(repo, ['--check', 'manifest', '--manifest', manifest])
    assert.equal(code, 1)
    assert.match(stdout, /no longer contains "forkOnlyHardening"/)
  })
})

describe('orphan check', () => {
  test('fails when a new dead exported symbol appears', () => {
    const repo = seedRepo({ takeUpstreamWholesale: false })
    const baseline = path.join(tmp, `orphans-${Math.random().toString(36).slice(2)}.json`)
    fs.writeFileSync(baseline, JSON.stringify({ go: [], i18n: [] }))

    let result = runGate(repo, ['--check', 'orphans', '--orphan-baseline', baseline])
    assert.equal(result.code, 0, result.stdout)

    fs.writeFileSync(
      path.join(repo, 'service', 'orphan.go'),
      'package service\n\nfunc NobodyCallsThis() {}\n'
    )
    result = runGate(repo, ['--check', 'orphans', '--orphan-baseline', baseline])
    assert.equal(result.code, 1, result.stdout)
    assert.match(result.stdout, /NobodyCallsThis/)

    // Once recorded as accepted, the same state passes again.
    result = runGate(repo, [
      '--check',
      'orphans',
      '--orphan-baseline',
      baseline,
      '--update-baseline',
    ])
    assert.equal(result.code, 0, result.stdout)
    result = runGate(repo, ['--check', 'orphans', '--orphan-baseline', baseline])
    assert.equal(result.code, 0, result.stdout)
  })
})

describe('i18n checks', () => {
  function i18nRepo() {
    const repo = fs.mkdtempSync(path.join(tmp, 'i18n-'))
    git(repo, ['init', '-q', '-b', 'main'])
    fs.mkdirSync(path.join(repo, 'web/src/i18n/locales'), { recursive: true })
    fs.mkdirSync(path.join(repo, 'web/src/i18n/overlay'), { recursive: true })
    for (const locale of ['en', 'zh']) {
      fs.writeFileSync(
        path.join(repo, 'web/src/i18n/locales', `${locale}.json`),
        JSON.stringify({ translation: { Greeting: 'Hello' } }, null, 2) + '\n'
      )
      fs.writeFileSync(
        path.join(repo, 'web/src/i18n/overlay', `${locale}.json`),
        JSON.stringify(
          { translation: { 'Invoice Total': 'Invoice Total' }, overrides: {} },
          null,
          2
        ) + '\n'
      )
    }
    fs.writeFileSync(
      path.join(repo, 'web/src/i18n/config.ts'),
      "import { FORK_LOCALE_BUNDLES } from './fork-bundles'\nexport const resources = FORK_LOCALE_BUNDLES\n"
    )
    git(repo, ['add', '.'])
    git(repo, ['commit', '-qm', 'seed'])
    git(repo, ['branch', 'upstream/main'])
    return repo
  }

  test('passes with untouched bundles and a matching overlay', () => {
    const repo = i18nRepo()
    const baseline = path.join(tmp, `locales-${Math.random().toString(36).slice(2)}.json`)
    let result = runGate(repo, [
      '--check',
      'i18n',
      '--locale-baseline',
      baseline,
      '--record-locales',
    ])
    assert.equal(result.code, 0, result.stdout)
    result = runGate(repo, ['--check', 'i18n', '--locale-baseline', baseline])
    assert.equal(result.code, 0, result.stdout)
  })

  test('fails when someone edits an upstream-owned bundle', () => {
    const repo = i18nRepo()
    const baseline = path.join(tmp, `locales-${Math.random().toString(36).slice(2)}.json`)
    runGate(repo, ['--check', 'i18n', '--locale-baseline', baseline, '--record-locales'])
    const file = path.join(repo, 'web/src/i18n/locales/en.json')
    fs.writeFileSync(
      file,
      JSON.stringify({ translation: { Greeting: 'Hello', 'Fork Only': 'Fork Only' } }, null, 2) + '\n'
    )
    const { code, stdout } = runGate(repo, ['--check', 'i18n', '--locale-baseline', baseline])
    assert.equal(code, 1, stdout)
    assert.match(stdout, /was edited \(hash differs from upstream\)/)
  })

  test('fails when upstream adopts a fork-only key', () => {
    const repo = i18nRepo()
    const baseline = path.join(tmp, `locales-${Math.random().toString(36).slice(2)}.json`)
    runGate(repo, ['--check', 'i18n', '--locale-baseline', baseline, '--record-locales'])
    // Upstream ships the same key in its own bundle: the overlay must give way
    // (or move to overrides), so the gate stops the silent double definition.
    git(repo, ['checkout', '-q', 'upstream/main'])
    for (const locale of ['en', 'zh']) {
      fs.writeFileSync(
        path.join(repo, 'web/src/i18n/locales', `${locale}.json`),
        JSON.stringify(
          { translation: { Greeting: 'Hello', 'Invoice Total': 'Invoice Total (upstream)' } },
          null,
          2
        ) + '\n'
      )
    }
    git(repo, ['commit', '-qam', 'upstream adds invoice total'])
    git(repo, ['checkout', '-q', 'main'])
    git(repo, ['checkout', 'upstream/main', '--', 'web/src/i18n/locales'])
    const { code, stdout } = runGate(repo, [
      '--check',
      'i18n',
      '--locale-baseline',
      baseline,
      '--record-locales',
    ])
    assert.equal(code, 1, stdout)
    assert.match(stdout, /upstream now ships 1 fork-only key\(s\): Invoice Total/)
  })

  test('flags a component that bypasses the overlay', () => {
    const repo = i18nRepo()
    const baseline = path.join(tmp, `locales-${Math.random().toString(36).slice(2)}.json`)
    runGate(repo, ['--check', 'i18n', '--locale-baseline', baseline, '--record-locales'])
    fs.mkdirSync(path.join(repo, 'web/src/pages'), { recursive: true })
    fs.writeFileSync(
      path.join(repo, 'web/src/pages/bad.ts'),
      "import en from '@/i18n/locales/en.json'\nexport const value = en.translation.Greeting\n"
    )
    const { code, stdout } = runGate(repo, ['--check', 'i18n', '--locale-baseline', baseline])
    assert.equal(code, 1, stdout)
    assert.match(stdout, /import the upstream-owned locale bundles directly/)
  })
})
