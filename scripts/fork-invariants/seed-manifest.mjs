/*
 * Draft generator for scripts/fork-invariants/manifest.json.
 *
 * It reads the tables in FORK-CHANGES.md, pulls the files out of each entry,
 * and picks anchors that are *fork-unique*: an identifier that exists in our
 * copy of the file but not in upstream's copy of the same file (or a file that
 * upstream does not have at all). Those anchors are what the gate asserts after
 * every upstream sync.
 *
 * The output is a DRAFT: a machine cannot tell which of two identifiers is the
 * load-bearing one. Review every entry (especially the ones FORK-CHANGES marks
 * 上游覆盖风险 高), add the anchors a human would pick, then commit.
 *
 * Usage:
 *   node scripts/fork-invariants/seed-manifest.mjs \
 *     [--repo .] [--upstream upstream/main] [--out scripts/fork-invariants/manifest.json]
 */
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'

const args = process.argv.slice(2)
function arg(name, fallback) {
  const i = args.indexOf(`--${name}`)
  return i === -1 ? fallback : args[i + 1]
}
const repo = path.resolve(arg('repo', '.'))
const upstream = arg('upstream', 'upstream/main')
const out = path.resolve(repo, arg('out', 'scripts/fork-invariants/manifest.json'))

const git = (argv) =>
  execFileSync('git', argv, {
    cwd: repo,
    encoding: 'utf8',
    maxBuffer: 1 << 30,
    stdio: ['ignore', 'pipe', 'ignore'],
  })

function upstreamFile(file) {
  try {
    return git(['show', `${upstream}:${file}`])
  } catch {
    return null
  }
}

const PATH_RE = /`([A-Za-z0-9_.@/-]+\.(?:go|ts|tsx|mjs|json|md|sh|yml|yaml|ps1))`/g

/** Index tracked files by basename so cells that name only a file still resolve. */
const BY_BASENAME = new Map()
for (const file of git(['ls-files']).split('\n')) {
  if (!file) continue
  const base = path.basename(file)
  if (!BY_BASENAME.has(base)) BY_BASENAME.set(base, [])
  BY_BASENAME.get(base).push(file)
}

function resolvePath(candidate) {
  if (fs.existsSync(path.join(repo, candidate))) return candidate
  const matches = BY_BASENAME.get(path.basename(candidate)) ?? []
  return matches.length === 1 ? matches[0] : null
}

/** Parse the FORK-CHANGES tables into {section, title, files, tests}. */
function parseForkChanges(text) {
  const entries = []
  let section = ''
  for (const line of text.split('\n')) {
    const heading = line.match(/^##\s+(.*)$/)
    if (heading) {
      section = heading[1].trim()
      continue
    }
    if (!line.startsWith('|') || /^\|\s*-+/.test(line) || /^\|\s*改造|^\|\s*契约/.test(line)) {
      continue
    }
    const cells = line
      .split('|')
      .slice(1, -1)
      .map((c) => c.trim())
    if (cells.length < 3) continue
    const title = cells[0].replace(/[*_]/g, '').trim()
    if (!title) continue
    const riskCell = cells[cells.length - 1]
    const risk = /^高/.test(riskCell)
      ? 'high'
      : /^中/.test(riskCell)
        ? 'medium'
        : /^低/.test(riskCell)
          ? 'low'
          : undefined
    const listCells = cells.slice(1, -1)
    const files = []
    const tests = []
    for (const cell of listCells) {
      for (const match of cell.matchAll(PATH_RE)) {
        const p = match[1]
        if (p.startsWith('docs/') || p.endsWith('.md')) continue
        const resolved = resolvePath(p)
        if (!resolved) continue
        if (/_test\.go$|\.test\.(ts|tsx)$|\/__tests__\//.test(resolved)) tests.push(resolved)
        else files.push(resolved)
      }
    }
    if (!files.length && !tests.length) continue
    entries.push({
      section,
      title: title.slice(0, 140),
      risk,
      files: [...new Set(files)],
      tests: [...new Set(tests)],
    })
  }
  return entries
}

/** Identifiers/literals defined in a source file, best-first. */
function candidateAnchors(file, source, upstreamSource) {
  const upstreamText = upstreamSource ?? ''
  const patterns = [
    [/^func\s+\([^)]*\)\s*([A-Za-z_][A-Za-z0-9_]*)\s*\(/gm, 1], // Go methods
    [/^func\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(/gm, 1], // Go functions
    [/^(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)/gm, 1],
    [/^export const ([A-Za-z_][A-Za-z0-9_]*)/gm, 1],
    [/^(?:export )?(?:const|let|var) ([A-Za-z_][A-Za-z0-9_]*)/gm, 1],
    [/^type ([A-Za-z_][A-Za-z0-9_]*)/gm, 1],
  ]
  const found = []
  for (const [pattern, group] of patterns) {
    for (const match of source.matchAll(pattern)) {
      const name = match[group]
      if (!name || name.length < 6) continue
      if (['func', 'return', 'string', 'number', 'boolean'].includes(name)) continue
      // Fork-unique: our copy has it, upstream's copy of the same file does not.
      if (upstreamText && upstreamText.includes(name)) continue
      found.push(name)
    }
  }
  if (upstreamSource === null) {
    // Brand-new fork file: the path itself is the anchor. Confirm it survived
    // every sync, because a whole-file take from upstream can only delete it.
    return [
      { kind: 'file', file },
      ...[...new Set(found)].slice(0, 2).map((name) => ({ kind: 'symbol', file, name })),
    ]
  }
  return [...new Set(found)].slice(0, 3).map((name) => ({ kind: 'symbol', file, name }))
}

const forkChanges = fs.readFileSync(path.join(repo, 'FORK-CHANGES.md'), 'utf8')
const parsed = parseForkChanges(forkChanges)

const entries = parsed.map((entry, index) => {
  const id =
    entry.title
      .toLowerCase()
      .replace(/[^a-z0-9\u4e00-\u9fff]+/g, '-')
      .replace(/^-|-$/g, '')
      .slice(0, 48) || `entry-${index + 1}`

  const anchors = []
  for (const file of entry.files.slice(0, 4)) {
    let source
    try {
      source = fs.readFileSync(path.join(repo, file), 'utf8')
    } catch {
      continue
    }
    const upstreamSource = upstreamFile(file)
    anchors.push(...candidateAnchors(file, source, upstreamSource))
  }

  const tests = entry.tests
    .filter((file) => fs.existsSync(path.join(repo, file)))
    .map((file) => {
      const source = fs.readFileSync(path.join(repo, file), 'utf8')
      const names = [...source.matchAll(/^func (Test[A-Za-z0-9_]+)\(/gm)].map(
        (m) => m[1]
      )
      const jsNames = [...source.matchAll(/(?:test|it)\(\s*['"`]([^'"`]{8,80})['"`]/g)].map(
        (m) => m[1]
      )
      const picked = names.length ? names : jsNames.slice(0, 2)
      return { file, names: picked }
    })
    .filter((t) => t.names.length > 0)

  return {
    id: `${id}-${index + 1}`,
    title: entry.title,
    ...(entry.risk ? { risk: entry.risk } : {}),
    source: `FORK-CHANGES.md › ${entry.section}`,
    files: entry.files,
    anchors,
    tests,
  }
})

const manifest = {
  $comment:
    'Machine-checked survival list for the fork changes inventoried in FORK-CHANGES.md. ' +
    'Run: node scripts/fork-invariants/main.mjs --check manifest. Add or fix anchors by hand; ' +
    'a dropped anchor after an upstream sync means the merge silently reverted fork behaviour.',
  upstreamRef: upstream,
  entries,
}

fs.mkdirSync(path.dirname(out), { recursive: true })
fs.writeFileSync(out, JSON.stringify(manifest, null, 2) + '\n', 'utf8')

const anchorCount = entries.reduce((n, e) => n + e.anchors.length, 0)
const testCount = entries.reduce((n, e) => n + e.tests.length, 0)
console.log(
  `seeded ${entries.length} entries, ${anchorCount} anchors, ${testCount} test files -> ${path.relative(repo, out)}`
)
