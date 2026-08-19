import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { spawnSync } from 'node:child_process'

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
for (const file of collectFiles(path.join(root, 'src'))) {
  const source = readFileSync(file, 'utf8')
  const relativeFile = path.relative(root, file)
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
  if (result.error) throw result.error
  if (result.status !== 0) process.exit(result.status ?? 1)
}

if (vitestFiles.length > 0) run(['x', 'vitest', 'run', ...vitestFiles])
if (nodeTestFiles.length > 0) run(['test', ...nodeTestFiles])
