#!/usr/bin/env node
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
 * Fork invariants gate.
 *
 * The upstream sync used to be checked by hand ("walk FORK-CHANGES.md, run the
 * three mechanical checks") and that ritual failed twice: rc35 silently dropped
 * five fork changes and rc39 dropped the whole video-token hardening block,
 * both with an all-green test suite. This turns the ritual into a gate.
 *
 * Checks
 *   manifest  every fork change in FORK-CHANGES.md still has its anchors
 *   i18n      upstream-owned locale bundles untouched + overlay invariants
 *   orphans   no new dead locale key / dead exported Go symbol
 *   merge     a merge commit did not take upstream's side over a fork change
 *   history   lines added by fork-only commits still exist (advisory)
 *
 * Usage
 *   node scripts/fork-invariants/main.mjs                      # everything
 *   node scripts/fork-invariants/main.mjs --check merge --merge <sha>
 *   node scripts/fork-invariants/main.mjs --update-baseline
 *   node scripts/fork-invariants/main.mjs --record-locales
 * See scripts/fork-invariants/README.md.
 */
import { execFileSync } from 'node:child_process'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

/* ------------------------------------------------------------------ args */

const argv = process.argv.slice(2)
function flag(name) {
  return argv.includes(`--${name}`)
}
function option(name, fallback) {
  const i = argv.indexOf(`--${name}`)
  return i === -1 ? fallback : argv[i + 1]
}
function options(name) {
  const out = []
  argv.forEach((value, i) => {
    if (value === `--${name}`) out.push(argv[i + 1])
  })
  return out
}

const repo = path.resolve(option('repo', path.join(HERE, '..', '..')))
// Overridable so the gate can be tested against a synthetic repository.
const manifestPath = path.resolve(option('manifest', path.join(HERE, 'manifest.json')))
const localeBaselinePath = path.resolve(
  option('locale-baseline', path.join(HERE, 'upstream-locales.json'))
)
const orphanBaselinePath = path.resolve(
  option('orphan-baseline', path.join(HERE, 'orphan-baseline.json'))
)
const mergeAllowlistPath = path.resolve(
  option('merge-allowlist', path.join(HERE, 'merge-allowlist.json'))
)
const upstreamRef = option('upstream', 'upstream/main')
const baseRef = option('base', 'origin/tokeness/main')
const jsonOut = flag('json')
const checks = options('check').length
  ? options('check').flatMap((value) => value.split(',')).filter(Boolean)
  : ['all']
const wanted = (name) => checks.includes('all') || checks.includes(name)

/* ------------------------------------------------------------------- git */

function git(args, { allowFailure = false } = {}) {
  try {
    return execFileSync('git', args, {
      cwd: repo,
      encoding: 'utf8',
      maxBuffer: 1 << 30,
      stdio: ['ignore', 'pipe', allowFailure ? 'ignore' : 'inherit'],
    })
  } catch (error) {
    if (allowFailure) return null
    throw error
  }
}

const refExists = (ref) => git(['rev-parse', '--verify', '--quiet', ref], { allowFailure: true }) !== null
const hasUpstream = refExists(upstreamRef)

function readTree(ref) {
  const raw = execFileSync('git', ['ls-tree', '-r', '-z', ref], {
    cwd: repo,
    maxBuffer: 1 << 30,
  }).toString('utf8')
  const tree = new Map()
  for (const entry of raw.split('\0')) {
    if (!entry) continue
    const [meta, file] = entry.split('\t')
    tree.set(file, meta.split(/\s+/)[2])
  }
  return tree
}

function upstreamFile(file) {
  if (!hasUpstream) return null
  return git(['show', `${upstreamRef}:${file}`], { allowFailure: true })
}

function upstreamBlob(file) {
  if (!hasUpstream) return null
  const sha = git(['rev-parse', `${upstreamRef}:${file}`], { allowFailure: true })
  return sha ? sha.trim() : null
}

/* ---------------------------------------------------------------- helpers */

const results = []
function record(check, ok, title, details = [], data = undefined) {
  results.push({ check, ok, title, details, ...(data === undefined ? {} : { data }) })
}
const sha256 = (buffer) => crypto.createHash('sha256').update(buffer).digest('hex')

function listFiles(dir, filter, out = []) {
  if (!fs.existsSync(dir)) return out
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules' || entry.name === 'dist') continue
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) listFiles(full, filter, out)
    else if (filter(full)) out.push(full)
  }
  return out
}

/**
 * Keys declared at depth 1 of a JSON object, in file order, duplicates
 * included. JSON.parse cannot see a duplicate key, and FORK-CHANGES §7
 * requires the locale bundles to have zero of them.
 */
function topLevelKeys(text) {
  const keys = []
  let depth = 0
  let i = 0
  let pending = null
  while (i < text.length) {
    const ch = text[i]
    if (ch === '"') {
      let end = i + 1
      let value = ''
      while (end < text.length) {
        if (text[end] === '\\') {
          value += text[end + 1]
          end += 2
          continue
        }
        if (text[end] === '"') break
        value += text[end]
        end += 1
      }
      let j = end + 1
      while (j < text.length && /\s/.test(text[j])) j += 1
      if (depth === 1 && text[j] === ':') pending = value
      i = end + 1
      continue
    }
    if (ch === '{' || ch === '[') depth += 1
    else if (ch === '}' || ch === ']') depth -= 1
    else if (ch === ',' && depth === 1 && pending !== null) {
      keys.push(pending)
      pending = null
    }
    i += 1
  }
  if (pending !== null) keys.push(pending)
  return keys
}

function readJson(file) {
  return JSON.parse(fs.readFileSync(file, 'utf8'))
}

/* ------------------------------------------------------- check: manifest */

function checkManifest() {
  const manifest = readJson(manifestPath)
  const missing = []
  const absorbed = []
  let anchors = 0

  for (const entry of manifest.entries) {
    if (entry.status === 'absorbed-upstream') {
      absorbed.push(`${entry.id} (${entry.supersededBy ?? 'upstream'})`)
      continue
    }
    for (const file of entry.files ?? []) {
      if (!fs.existsSync(path.join(repo, file))) {
        missing.push(`${entry.id}: file ${file} is gone`)
      }
    }
    for (const anchor of entry.anchors ?? []) {
      anchors += 1
      const target = path.join(repo, anchor.file)
      if (!fs.existsSync(target)) {
        missing.push(`${entry.id}: anchor file ${anchor.file} is gone`)
        continue
      }
      const source = fs.readFileSync(target, 'utf8')
      if (anchor.kind === 'file') continue
      const needle = anchor.name ?? anchor.contains
      const found = anchor.regex
        ? new RegExp(anchor.regex, 'm').test(source)
        : source.includes(needle)
      if (!found) {
        missing.push(
          `${entry.id}: ${anchor.file} no longer contains ${anchor.regex ?? JSON.stringify(needle)}` +
            (anchor.note ? ` — ${anchor.note}` : '')
        )
      }
    }
    for (const test of entry.tests ?? []) {
      const target = path.join(repo, test.file)
      if (!fs.existsSync(target)) {
        missing.push(`${entry.id}: test file ${test.file} is gone`)
        continue
      }
      const source = fs.readFileSync(target, 'utf8')
      for (const name of test.names ?? []) {
        const present = test.file.endsWith('.go')
          ? new RegExp(`func\\s+${name}\\s*\\(`).test(source)
          : source.includes(name)
        if (!present) missing.push(`${entry.id}: ${test.file} lost test ${name}`)
      }
    }
  }

  const detail = [
    `${manifest.entries.length} entries, ${anchors} anchors checked`,
    ...(absorbed.length ? [`skipped (absorbed upstream): ${absorbed.join(', ')}`] : []),
    ...missing,
  ]
  record('manifest', missing.length === 0, 'FORK-CHANGES survival manifest', detail)
}

/* ----------------------------------------------------------- check: i18n */

const I18N_DIRECT_IMPORT_ALLOWLIST = new Set([
  'web/src/i18n/fork-bundles.ts',
  'web/src/i18n/overlay.ts',
])

function localeFiles() {
  const dir = path.join(repo, 'web/src/i18n/locales')
  return fs
    .readdirSync(dir)
    .filter((name) => name.endsWith('.json'))
    .sort()
}

function overlayFiles() {
  const dir = path.join(repo, 'web/src/i18n/overlay')
  if (!fs.existsSync(dir)) return []
  return fs
    .readdirSync(dir)
    .filter((name) => name.endsWith('.json'))
    .sort()
}

/** Facts about one locale that only upstream can answer, cached in the baseline. */
function localeUpstreamFacts(locale, overlay) {
  const upstreamBundle = upstreamFile(`web/src/i18n/locales/${locale}`)
  if (!upstreamBundle) return null
  const upstreamKeys = new Set(Object.keys(JSON.parse(upstreamBundle).translation ?? {}))
  const forkKeys = Object.keys(overlay.translation ?? {})
  const overrides = Object.keys(overlay.overrides ?? {})
  return {
    upstreamKeyCount: upstreamKeys.size,
    keySetHash: sha256([...upstreamKeys].sort().join('\n')),
    forkKeyCollisions: forkKeys.filter((key) => upstreamKeys.has(key)).sort(),
    staleOverrides: overrides.filter((key) => !upstreamKeys.has(key)).sort(),
  }
}

function checkI18n() {
  const problems = []
  const notes = []
  const locales = localeFiles()
  const overlays = overlayFiles()

  if (overlays.length === 0) {
    record('i18n', false, 'locale overlay invariants', ['web/src/i18n/overlay/ is missing'])
    return
  }

  const bundles = {}
  const parsedOverlays = {}
  const currentHashes = {}
  for (const name of locales) {
    const file = path.join(repo, 'web/src/i18n/locales', name)
    currentHashes[name] = sha256(fs.readFileSync(file))
    bundles[name] = JSON.parse(fs.readFileSync(file, 'utf8'))
  }
  for (const name of overlays) {
    parsedOverlays[name] = readJson(path.join(repo, 'web/src/i18n/overlay', name))
  }

  const recorded = {}
  for (const name of locales) {
    const overlay = parsedOverlays[name] ?? { translation: {}, overrides: {} }
    const facts = hasUpstream ? localeUpstreamFacts(name, overlay) : null
    if (flag('record-locales')) {
      if (!hasUpstream) {
        problems.push(`cannot record locales: ${upstreamRef} is not available`)
        break
      }
      recorded[name] = { sha256: currentHashes[name], ...facts }
    }
  }

  if (flag('record-locales') && problems.length === 0) {
    fs.writeFileSync(
      localeBaselinePath,
      JSON.stringify(
        {
          $comment:
            'Upstream-owned locale bundles (web/src/i18n/locales/*.json): sha256 of the upstream ' +
            'bytes plus the upstream facts the offline gate needs (key-set hash, key count, and any ' +
            'collision between an upstream key and a fork overlay key). Every fork string lives in ' +
            'web/src/i18n/overlay/ instead. Refresh with: node scripts/fork-invariants/main.mjs --record-locales',
          upstreamRef,
          recordedAt: new Date().toISOString().slice(0, 10),
          files: recorded,
        },
        null,
        2
      ) + '\n',
      'utf8'
    )
    notes.push(`recorded ${locales.length} locale bundle(s) from ${upstreamRef}`)
  }

  const baseline = fs.existsSync(localeBaselinePath) ? readJson(localeBaselinePath) : null
  if (!baseline) {
    problems.push('upstream-locales.json is missing; run --record-locales with upstream available')
  } else {
    for (const name of locales) {
      const entry = baseline.files?.[name]
      const expected = typeof entry === 'string' ? entry : entry?.sha256
      if (!expected) {
        problems.push(`no recorded upstream hash for ${name}; run --record-locales`)
        continue
      }
      if (expected !== currentHashes[name]) {
        problems.push(
          `web/src/i18n/locales/${name} was edited (hash differs from upstream). Locale bundles are ` +
            'upstream-owned: put fork strings in web/src/i18n/overlay/ instead. If this is the sync ' +
            'taking upstream\'s bundle, re-record with --record-locales.'
        )
      }
      // Facts recorded from upstream: a non-empty list is an unresolved decision.
      const facts = typeof entry === 'string' ? null : entry
      if (facts?.forkKeyCollisions?.length) {
        problems.push(
          `${name}: upstream already ships fork-only key(s) ${facts.forkKeyCollisions.slice(0, 5).join(', ')} — ` +
            'drop them from the overlay or move them to "overrides"'
        )
      }
      if (facts?.staleOverrides?.length) {
        problems.push(
          `${name}: override(s) no longer exist upstream: ${facts.staleOverrides.slice(0, 5).join(', ')}`
        )
      }
    }
  }

  // Overlay shape invariants.
  const reference = overlays[0]
  const referenceForkKeys = Object.keys(parsedOverlays[reference]?.translation ?? {}).sort()
  const overrideKeys = new Set()

  for (const name of overlays) {
    const file = path.join(repo, 'web/src/i18n/overlay', name)
    const duplicated = topLevelKeys(fs.readFileSync(file, 'utf8')).filter(
      (key, index, all) => all.indexOf(key) !== index || all.lastIndexOf(key) !== index
    )
    if (duplicated.length) {
      problems.push(
        `web/src/i18n/overlay/${name}: duplicate key(s) ${[...new Set(duplicated)].join(', ')}`
      )
    }

    const overlay = parsedOverlays[name]
    const forkKeys = Object.keys(overlay.translation ?? {}).sort()
    const overrides = Object.keys(overlay.overrides ?? {})
    for (const key of overrides) overrideKeys.add(key)

    if (forkKeys.join('\u0000') !== referenceForkKeys.join('\u0000')) {
      const onlyHere = forkKeys.filter((key) => !referenceForkKeys.includes(key))
      const absent = referenceForkKeys.filter((key) => !forkKeys.includes(key))
      problems.push(
        `web/src/i18n/overlay/${name}: fork-only key set differs from ${reference}` +
          (onlyHere.length ? ` (+${onlyHere.slice(0, 5).join(', ')})` : '') +
          (absent.length ? ` (-${absent.slice(0, 5).join(', ')})` : '')
      )
    }
    for (const key of forkKeys) {
      if (key in (overlay.overrides ?? {})) problems.push(`${name}: ${key} is both a fork key and an override`)
      if (!overlay.translation[key]) problems.push(`${name}: ${key} has an empty value`)
    }
    for (const key of overrides) {
      if (!overlay.overrides[key]) problems.push(`${name}: override ${key} has an empty value`)
    }

    // Live upstream comparison when the ref is available (local run or sync PR).
    if (hasUpstream) {
      const facts = localeUpstreamFacts(name, overlay)
      if (facts?.forkKeyCollisions.length) {
        problems.push(
          `web/src/i18n/overlay/${name}: upstream now ships ${facts.forkKeyCollisions.length} ` +
            `fork-only key(s): ${facts.forkKeyCollisions.slice(0, 5).join(', ')} — drop them here or move ` +
            'them to "overrides"'
        )
      }
      if (facts?.staleOverrides.length) {
        problems.push(
          `web/src/i18n/overlay/${name}: ${facts.staleOverrides.length} override(s) no longer exist ` +
            `upstream: ${facts.staleOverrides.slice(0, 5).join(', ')}`
        )
      }
    }
  }

  // Wiring: the app must compose the overlay, and nothing may bypass it.
  const config = fs.readFileSync(path.join(repo, 'web/src/i18n/config.ts'), 'utf8')
  if (!config.includes('FORK_LOCALE_BUNDLES')) {
    problems.push('web/src/i18n/config.ts no longer composes FORK_LOCALE_BUNDLES (fork strings would vanish)')
  }
  const bypass = listFiles(
    path.join(repo, 'web/src'),
    (file) => /\.tsx?$/.test(file)
  )
    .map((file) => path.relative(repo, file))
    .filter((rel) =>
      /from\s+['"][^'"]*i18n\/locales\//.test(fs.readFileSync(path.join(repo, rel), 'utf8'))
    )
    .filter((rel) => !I18N_DIRECT_IMPORT_ALLOWLIST.has(rel))
  if (bypass.length) {
    problems.push(
      `these files import the upstream-owned locale bundles directly instead of @/i18n/fork-bundles: ${bypass.join(', ')}`
    )
  }

  const detail = [
    `${locales.length} upstream bundles hash-checked`,
    `${overlays.length} overlays, ${referenceForkKeys.length} fork-only keys per locale, ` +
      `${overrideKeys.size} re-worded upstream key(s) across locales`,
    hasUpstream ? `live upstream comparison against ${upstreamRef}` : 'offline: recorded upstream facts only',
    ...notes,
    ...problems,
  ]
  record('i18n', problems.length === 0, 'locale overlay invariants', detail)
}

/* -------------------------------------------------------- check: orphans */

const GO_SKIP_DIRS = new Set(['node_modules', 'web', 'docs', '.git', 'electron', 'e2e'])

function goFiles() {
  const files = []
  const stack = [repo]
  while (stack.length) {
    const dir = stack.pop()
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) {
        if (GO_SKIP_DIRS.has(entry.name)) continue
        stack.push(path.join(dir, entry.name))
      } else if (entry.name.endsWith('.go')) {
        files.push(path.join(dir, entry.name))
      }
    }
  }
  return files
}

function exportedGoSymbols(file, source) {
  const names = []
  for (const pattern of [
    /^func\s+\([^)]*\)\s*([A-Z][A-Za-z0-9_]*)\s*\(/gm,
    /^func\s+([A-Z][A-Za-z0-9_]*)\s*\(/gm,
    /^type\s+([A-Z][A-Za-z0-9_]*)\b/gm,
    /^(?:var|const)\s+([A-Z][A-Za-z0-9_]*)\b/gm,
  ]) {
    for (const match of source.matchAll(pattern)) names.push(match[1])
  }
  return names
}

function findOrphans() {
  const files = goFiles()
  const declaringFile = new Map()
  const fileCount = new Map()
  const selfUse = new Map()

  for (const file of files) {
    const source = fs.readFileSync(file, 'utf8')
    const identifiers = new Set(source.match(/[A-Za-z_][A-Za-z0-9_]*/g) ?? [])
    for (const identifier of identifiers) {
      fileCount.set(identifier, (fileCount.get(identifier) ?? 0) + 1)
    }
    if (file.endsWith('_test.go')) continue
    const rel = path.relative(repo, file)
    const declarations = new Map()
    for (const name of exportedGoSymbols(file, source)) {
      declarations.set(name, (declarations.get(name) ?? 0) + 1)
      if (!declaringFile.has(name)) declaringFile.set(name, rel)
    }
    // A symbol whose only mentions are its own declaration(s) is dead weight;
    // one used inside its own file is not an orphan in the sense we care about.
    for (const [name, count] of declarations) {
      const occurrences = (source.match(new RegExp(`\\b${name}\\b`, 'g')) ?? []).length
      selfUse.set(name, occurrences - count)
    }
  }

  const goOrphans = []
  for (const [name, rel] of declaringFile) {
    const usedElsewhere = (fileCount.get(name) ?? 0) > 1
    const usedInOwnFile = (selfUse.get(name) ?? 0) > 0
    if (!usedElsewhere && !usedInOwnFile) goOrphans.push(`${name} (${rel})`)
  }

  // Locale keys with no literal in the frontend sources: upstream keys are the
  // upstream team's problem, fork overlay keys are ours.
  const webSources = listFiles(
    path.join(repo, 'web/src'),
    (file) => /\.(ts|tsx)$/.test(file) && !file.includes('/i18n/locales/') && !file.includes('/i18n/overlay/')
  )
  const haystack = webSources.map((file) => fs.readFileSync(file, 'utf8')).join('\n')

  const i18nOrphans = []
  for (const name of overlayFiles()) {
    const overlay = readJson(path.join(repo, 'web/src/i18n/overlay', name))
    for (const key of Object.keys(overlay.translation ?? {})) {
      const probe = key.length > 24 ? key.slice(0, 24) : key
      if (!haystack.includes(probe)) i18nOrphans.push(key)
    }
  }

  return { goOrphans: [...new Set(goOrphans)].sort(), i18nOrphans: [...new Set(i18nOrphans)].sort() }
}

function checkOrphans() {
  const baselinePath = orphanBaselinePath
  const { goOrphans, i18nOrphans } = findOrphans()

  if (flag('update-baseline')) {
    fs.writeFileSync(
      baselinePath,
      JSON.stringify(
        {
          $comment:
            'Accepted orphans at the time this baseline was recorded: Go symbols referenced only from ' +
            'their own file, and overlay locale keys with no literal in web/src. A new entry means a ' +
            'merge dropped a consumer. Regenerate deliberately: node scripts/fork-invariants/main.mjs --update-baseline',
          recordedAt: new Date().toISOString().slice(0, 10),
          go: goOrphans,
          i18n: i18nOrphans,
        },
        null,
        2
      ) + '\n',
      'utf8'
    )
    record('orphans', true, 'orphan baseline', [
      `baseline updated: ${goOrphans.length} Go symbols, ${i18nOrphans.length} locale keys`,
    ])
    return
  }

  const baseline = fs.existsSync(baselinePath) ? readJson(baselinePath) : { go: [], i18n: [] }
  const knownGo = new Set(baseline.go ?? [])
  const knownI18n = new Set(baseline.i18n ?? [])
  const newGo = goOrphans.filter((item) => !knownGo.has(item))
  const newI18n = i18nOrphans.filter((item) => !knownI18n.has(item))
  const fixedGo = [...knownGo].filter((item) => !goOrphans.includes(item))
  const fixedI18n = [...knownI18n].filter((item) => !i18nOrphans.includes(item))

  const detail = [
    `Go orphans ${goOrphans.length} (baseline ${knownGo.size}), locale-key orphans ${i18nOrphans.length} (baseline ${knownI18n.size})`,
    ...(newGo.length ? [`new Go orphans: ${newGo.slice(0, 8).join(', ')}`] : []),
    ...(newI18n.length ? [`new locale-key orphans: ${newI18n.slice(0, 8).join(', ')}`] : []),
    ...(fixedGo.length || fixedI18n.length
      ? [
          `fixed since baseline (${fixedGo.length + fixedI18n.length}) — shrink the baseline with --update-baseline`,
        ]
      : []),
  ]
  record('orphans', newGo.length === 0 && newI18n.length === 0, 'no new orphaned producers', detail)
}

/* ---------------------------------------------------------- check: merge */

function checkMerge() {
  const mergeArg = option('merge', 'HEAD')
  const sha = git(['rev-parse', mergeArg], { allowFailure: true })?.trim()
  if (!sha) {
    record('merge', false, 'merge divergence', [`cannot resolve ${mergeArg}`])
    return
  }
  const parents = git(['rev-list', '--parents', '-n', '1', sha]).trim().split(/\s+/).slice(1)
  if (parents.length < 2) {
    record('merge', true, 'merge divergence', [
      `${mergeArg} is not a merge commit — nothing to compare (ok for ordinary pushes)`,
    ])
    return
  }

  // The fork parent is the one that is not reachable from upstream; when
  // upstream is unavailable, assume the first parent is the fork side (that is
  // how `git merge upstream/main` records it).
  // Classify the parents. `git merge upstream/main` records the fork side
  // first, and the fork side is the one that is an ancestor of the base branch;
  // when neither test resolves we keep that default order.
  let forkParent = parents[0]
  let upstreamParent = parents[1]
  if (refExists(baseRef)) {
    const onBase = parents.map(
      (parent) =>
        git(['merge-base', '--is-ancestor', parent, baseRef], { allowFailure: true }) !== null
    )
    if (onBase[0] && !onBase[1]) {
      forkParent = parents[0]
      upstreamParent = parents[1]
    } else if (!onBase[0] && onBase[1]) {
      forkParent = parents[1]
      upstreamParent = parents[0]
    }
  } else if (hasUpstream) {
    const onUpstream = parents.map(
      (parent) =>
        git(['merge-base', '--is-ancestor', parent, upstreamRef], { allowFailure: true }) !== null
    )
    if (onUpstream[0] && !onUpstream[1]) {
      forkParent = parents[1]
      upstreamParent = parents[0]
    }
  }

  const result = readTree(sha)
  const forkTree = readTree(forkParent)
  const upstreamTree = readTree(upstreamParent)
  const allowlist = fs.existsSync(mergeAllowlistPath) ? readJson(mergeAllowlistPath) : { paths: [] }
  const allowed = new Set((allowlist.paths ?? []).map((entry) => entry.path ?? entry))

  const paths = new Set([...result.keys(), ...forkTree.keys(), ...upstreamTree.keys()])
  const dropped = []
  const droppedFiles = []

  const acknowledged = []
  for (const file of paths) {
    const inResult = result.get(file)
    const inFork = forkTree.get(file)
    const inUpstream = upstreamTree.get(file)
    if (!inFork) continue
    if (inResult === inFork) continue // fork content survived
    let suspicion = null
    if (inResult === inUpstream) {
      // The merge took upstream's side over a fork-modified path.
      const stats = git(['diff', '--numstat', upstreamParent, forkParent, '--', file], {
        allowFailure: true,
      })
      const added = Number((stats ?? '').trim().split(/\s+/)[0] || 0)
      if (Number.isFinite(added) && added > 0) suspicion = `${file} (+${added} fork lines lost)`
    } else if (!inResult) {
      suspicion = `${file} (file removed)`
    }
    if (!suspicion) continue
    if (allowed.has(file)) {
      acknowledged.push(suspicion)
      continue
    }
    if (suspicion.includes('file removed')) droppedFiles.push(suspicion)
    else dropped.push(suspicion)
  }

  const detail = [
    `merge ${sha.slice(0, 9)} (fork ${forkParent.slice(0, 9)}, upstream ${upstreamParent.slice(0, 9)}), ${paths.size} paths`,
    `took upstream over fork: ${dropped.length} path(s); deleted fork files: ${droppedFiles.length}`,
    ...dropped.slice(0, 20).map((item) => `  ${item}`),
    ...droppedFiles.slice(0, 20).map((item) => `  deleted: ${item}`),
    ...(dropped.length + droppedFiles.length > 20 ? [`  …and ${dropped.length + droppedFiles.length - 20} more (see --json)`] : []),
    ...(acknowledged.length ? [`acknowledged by merge-allowlist.json: ${acknowledged.length}`] : []),
    ...(dropped.length + droppedFiles.length
      ? ['Confirm each path by hand; if the fork behaviour moved elsewhere, add it to merge-allowlist.json with a reason.']
      : []),
  ]
  record(
    'merge',
    dropped.length === 0 && droppedFiles.length === 0,
    'merge did not drop fork work',
    detail,
    { merge: sha, forkParent, upstreamParent, tookUpstream: dropped, deletedForkFiles: droppedFiles }
  )
}

/* -------------------------------------------------------- check: history */

function checkHistory() {
  if (!hasUpstream) {
    record('history', true, 'fork-only commit survival', [`${upstreamRef} unavailable — skipped`])
    return
  }
  const mergeBase = git(['merge-base', upstreamRef, 'HEAD']).trim()
  const commits = git([
    'log',
    '--no-merges',
    '--format=%H',
    `${mergeBase}..HEAD`,
    '--not',
    upstreamRef,
  ])
    .trim()
    .split('\n')
    .filter(Boolean)

  if (commits.length === 0) {
    record('history', true, 'fork-only commit survival', ['no fork-only commits in range'])
    return
  }

  const tracked = new Set(readTree('HEAD').keys())
  const weak = []
  let scanned = 0

  for (const commit of commits.slice(0, 400)) {
    const diff = git(['show', '--format=', '--unified=0', '--no-color', commit], { allowFailure: true })
    if (!diff) continue
    let file = null
    const lines = []
    for (const line of diff.split('\n')) {
      if (line.startsWith('+++ b/')) {
        file = line.slice(6)
        continue
      }
      if (!line.startsWith('+') || line.startsWith('+++')) continue
      if (!file || !tracked.has(file)) continue
      if (/\.(json|md|sum|lock)$/.test(file) || file.includes('/locales/')) continue
      const text = line.slice(1).trim()
      if (text.length < 40) continue
      if (/^(\/\/|#|\*|<!--)/.test(text)) continue
      lines.push({ file, text })
    }
    if (lines.length === 0) continue
    scanned += 1
    const byFile = new Map()
    for (const item of lines) {
      if (!byFile.has(item.file)) byFile.set(item.file, [])
      byFile.get(item.file).push(item.text)
    }
    let survivors = 0
    for (const [target, texts] of byFile) {
      const source = fs.readFileSync(path.join(repo, target), 'utf8')
      for (const text of texts) if (source.includes(text)) survivors += 1
    }
    const ratio = survivors / lines.length
    if (ratio < 0.34) {
      const subject = git(['log', '-1', '--format=%h %s', commit]).trim()
      weak.push(`${subject} — ${survivors}/${lines.length} added lines still present (${Math.round(ratio * 100)}%)`)
    }
  }

  const blocking = flag('fail-on-history-drop')
  const detail = [
    `scanned ${scanned} fork-only commit(s) with distinctive added lines`,
    ...weak.slice(0, 15),
    ...(weak.length
      ? [blocking ? 'FAIL: low survival' : 'advisory: a low ratio can be legitimate (rewritten code), check the list']
      : []),
  ]
  record('history', !blocking || weak.length === 0, 'fork-only commit survival', detail)
}

/* ------------------------------------------------------------------- run */

const plan = []
if (wanted('manifest')) plan.push(checkManifest)
if (wanted('i18n')) plan.push(checkI18n)
if (wanted('orphans')) plan.push(checkOrphans)
if (wanted('merge')) plan.push(checkMerge)
if (wanted('history')) plan.push(checkHistory)

for (const check of plan) check()

const failed = results.filter((result) => !result.ok)

if (jsonOut) {
  console.log(JSON.stringify({ repo, upstreamRef, hasUpstream, results, ok: failed.length === 0 }, null, 2))
} else {
  console.log(`fork invariants @ ${repo}`)
  console.log(`upstream ref: ${hasUpstream ? upstreamRef : `${upstreamRef} (unavailable, some checks degraded)`}`)
  for (const result of results) {
    console.log(`\n${result.ok ? 'PASS' : 'FAIL'}  ${result.title}`)
    for (const line of result.details) console.log(`      ${line}`)
  }
  console.log(
    `\n${results.length - failed.length}/${results.length} checks passed` +
      (failed.length ? ` — FAILED: ${failed.map((r) => r.check).join(', ')}` : '')
  )
}

process.exitCode = failed.length === 0 ? 0 : 1
