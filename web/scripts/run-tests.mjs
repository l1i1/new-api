import { spawnSync } from 'node:child_process'
import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'

const root = process.cwd()
const testFilePattern = /\.(test|spec)\.(ts|tsx)$/

function collectFiles(directory) {
  const files = []
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const fullPath = path.join(directory, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== 'node_modules' && entry.name !== 'dist') {
        files.push(...collectFiles(fullPath))
      }
      continue
    }
    if (testFilePattern.test(entry.name)) files.push(fullPath)
  }
  return files
}

const vitestFiles = []
const nodeTestFiles = []
const testFiles = ['src', 'scripts'].flatMap((directory) =>
  collectFiles(path.join(root, directory))
)
for (const file of testFiles) {
  const source = readFileSync(file, 'utf8')
  const relativeFile = `./${path.relative(root, file).split(path.sep).join('/')}`
  if (/from\s+['"]vitest['"]/.test(source)) {
    vitestFiles.push(relativeFile)
  } else if (/from\s+['"]node:test['"]/.test(source)) {
    nodeTestFiles.push(relativeFile)
  }
}

function run(args) {
  const result = spawnSync(process.execPath, args, {
    cwd: root,
    stdio: 'inherit',
  })
  if (result.error) console.error(result.error)
  return result.status ?? 1
}

let exitCode = 0
if (vitestFiles.length > 0) {
  exitCode = run(['x', 'vitest', 'run', ...vitestFiles]) || exitCode
}
if (nodeTestFiles.length > 0) {
  exitCode =
    run([
      'test',
      '--preload',
      './scripts/node-test-setup.ts',
      ...nodeTestFiles,
    ]) || exitCode
}
process.exitCode = exitCode
