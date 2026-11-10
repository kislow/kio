import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse, stringify } from 'yaml'

export const ROOT = dirname(dirname(fileURLToPath(import.meta.url)))
export const SPECS = ['vault-pusher', 'ki-cli', 'kio-architecture']
export const specFile = (name) => join(ROOT, 'testdata', 'specs', `${name}.diagram.yaml`)
export const readSpec = (name) => parse(readFileSync(specFile(name), 'utf8'))
export const golden = (name) => JSON.parse(readFileSync(join(ROOT, 'testdata', 'golden', `${name}.json`), 'utf8'))

export function tempDir(t) {
  const dir = mkdtempSync(join(tmpdir(), 'ki-diagram-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  return dir
}

export function writeSpec(dir, name, spec) {
  const path = join(dir, `${name}.diagram.yaml`)
  writeFileSync(path, stringify(spec))
  return path
}

// The shapes on each parent, bottom to top.
export function stacking(records) {
  const by = {}
  for (const r of records) if ('index' in r && 'parentId' in r) (by[r.parentId] ??= []).push(r)
  for (const list of Object.values(by)) list.sort((a, b) => (a.index < b.index ? -1 : a.index > b.index ? 1 : 0))
  return Object.fromEntries(Object.entries(by).map(([k, v]) => [k, v.map((r) => r.id)]))
}

export const withoutIndex = (records) => new Map(records.map(({ index, ...r }) => [r.id, r]))
