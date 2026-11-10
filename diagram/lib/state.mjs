// The snapshot of what ki-diagram last wrote for a drawing, kept in a hidden
// .ki-diagram folder next to it (with the spec). It is the base of the
// three-way merge: a difference between it and the drawing is a hand edit.
// Obsidian Sync skips dot folders, so both travel through the vault's git.
import { existsSync, readFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { writeAtomic } from './drawing.mjs'
import { isContent, migrate } from './tldraw.mjs'

export const STATE_DIR = '.ki-diagram'
const FORMAT = 1

export const statePath = (drawingPath) => join(dirname(drawingPath), STATE_DIR, basename(drawingPath).replace(/\.md$/, '') + '.base.json')

export function readState(drawingPath) {
  const path = statePath(drawingPath)
  if (!existsSync(path)) return null
  let state
  try {
    state = JSON.parse(readFileSync(path, 'utf8'))
  } catch (err) {
    throw new Error(`${path} is not valid JSON (${err.message}); delete it and rebuild with --adopt or --base`)
  }
  if (state['ki-diagram'] !== FORMAT || !Array.isArray(state.records)) throw new Error(`${path} is not a ki-diagram snapshot`)
  return migrate(state.schema, state.records).filter(isContent)
}

// One record per line, so the vault's git history shows what a rebuild changed.
export function writeState(drawingPath, schema, records) {
  const lines = records.filter(isContent).map((r) => JSON.stringify(r))
  writeAtomic(statePath(drawingPath), `{"ki-diagram":${FORMAT},"schema":${JSON.stringify(schema)},"records":[\n${lines.join(',\n')}\n]}\n`)
}
