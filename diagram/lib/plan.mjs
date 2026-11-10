// Works out what building a spec would do to its drawing, without writing:
// a new drawing, nothing, spec changes merged around hand edits, or a refusal.
import { existsSync, readFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { isDeepStrictEqual } from 'node:util'
import { buildRecords } from './build.mjs'
import { formatDrawing, frontmatter, hashText, readDrawing, writeAtomic } from './drawing.mjs'
import { mergeRecords, same } from './merge.mjs'
import { lintSpec, loadSpec } from './spec.mjs'
import { STATE_DIR, readState, writeState } from './state.mjs'
import { isContent, migrate, serializedSchema, validated } from './tldraw.mjs'

// <dir>/.ki-diagram/<name>.diagram.yaml draws <dir>/<name>.md; a spec kept
// anywhere else draws next to itself.
export function drawingFor(specPath) {
  const m = /^(.*)\.diagram\.(ya?ml|json)$/.exec(basename(specPath))
  if (!m) throw new Error(`${specPath}: name the spec <drawing>.diagram.yaml, or pass --drawing`)
  const dir = dirname(specPath)
  return join(basename(dir) === STATE_DIR ? dirname(dir) : dir, `${m[1]}.md`)
}

// status: new | up-to-date | update | conflict | no-base
export function plan({ specPath, drawingPath = drawingFor(specPath), basePath, adopt = false, prefer }) {
  const spec = loadSpec(specPath)
  const generated = buildRecords(spec)
  const fromSpec = generated.filter(isContent)
  const schema = serializedSchema()
  const p = { specPath, drawingPath, warnings: lintSpec(spec), spec: fromSpec, schema, applied: [], kept: [], conflicts: [] }

  if (!existsSync(drawingPath)) {
    const text = formatDrawing({ before: frontmatter(spec.tags ?? []), schema, records: generated })
    return { ...p, status: 'new', text, writeState: true, pages: spec.pages.length, shapes: fromSpec.filter((r) => r.typeName === 'shape').length }
  }

  const drawing = readDrawing(drawingPath)
  const current = migrate(drawing.raw.schema, drawing.raw.records)
  const fromDrawing = current.filter(isContent)
  let base = basePath ? migrate(...rawOf(basePath)).filter(isContent) : readState(drawingPath)
  const stateMissing = base === null && !basePath
  if (base === null) {
    const differing = differences(fromDrawing, fromSpec)
    if (!adopt && differing.length) return { ...p, status: 'no-base', differing }
    // Adopting: whatever differs from the spec counts as a hand edit and stays.
    base = fromSpec
  }

  const merged = mergeRecords({ base, drawing: fromDrawing, spec: fromSpec, prefer })
  Object.assign(p, { applied: merged.applied, kept: merged.kept, conflicts: merged.conflicts })
  if (merged.conflicts.some((c) => c.unresolvable || !c.resolved)) return { ...p, status: 'conflict' }

  const records = validated(withSession(merged.records, current))
  const unchanged = isDeepStrictEqual(records, drawing.raw.records) && isDeepStrictEqual(schema, drawing.raw.schema)
  const stateStale = stateMissing || basePath !== undefined || !isDeepStrictEqual(readState(drawingPath), fromSpec)
  if (unchanged) return { ...p, status: 'up-to-date', writeState: stateStale }
  const text = formatDrawing({ before: drawing.before, after: drawing.after, meta: drawing.meta, schema, records })
  return { ...p, status: 'update', text, hash: drawing.hash, writeState: true }
}

const rawOf = (path) => { const d = readDrawing(path); return [d.raw.schema, d.raw.records] }

function differences(a, b) {
  const ma = new Map(a.map((r) => [r.id, r]))
  const mb = new Map(b.map((r) => [r.id, r]))
  return [...new Set([...ma.keys(), ...mb.keys()])].filter((id) => !same(ma.get(id), mb.get(id)))
}

// Content records from the merge, with the drawing's session records
// (cameras, pointer, user) left where they were.
function withSession(content, current) {
  const merged = new Map(content.map((r) => [r.id, r]))
  const out = []
  for (const r of current) {
    if (!isContent(r)) out.push(r)
    else if (merged.has(r.id)) { out.push(merged.get(r.id)); merged.delete(r.id) }
  }
  return [...out, ...merged.values()]
}

// Carries out a plan. The drawing is re-read first: if Obsidian saved it in
// the meantime, nothing is written. The drawing goes before the snapshot, so
// a failure in between leaves spec changes looking applied, never hand edits
// looking like spec changes.
export function apply(p) {
  if (p.status === 'update') {
    if (hashText(readFileSync(p.drawingPath, 'utf8')) !== p.hash) throw new Error(`${p.drawingPath} changed while it was being merged; run the build again`)
  }
  if (p.status === 'new' || p.status === 'update') writeAtomic(p.drawingPath, p.text)
  if (p.writeState) writeState(p.drawingPath, p.schema, p.spec)
}

const MAX_LINES = 25

// What a plan did or would do, as lines for the terminal.
export function describePlan(p, { dryRun = false } = {}) {
  const would = (done, pending) => (dryRun ? pending : done)
  const lines = p.warnings.map((w) => `${p.specPath}: warning: ${w}`)
  const name = p.drawingPath
  const list = (label, items, fmt) => {
    items.slice(0, MAX_LINES).forEach((i) => lines.push(`  ${label} ${fmt(i)}`))
    if (items.length > MAX_LINES) lines.push(`  ... and ${items.length - MAX_LINES} more`)
  }
  const edits = p.kept.length ? `, keeping ${p.kept.length} hand edit(s)` : ''
  switch (p.status) {
    case 'new':
      lines.push(`${name}: ${would('wrote', 'would write')} a new drawing, ${p.pages} page(s), ${p.shapes} shapes`)
      break
    case 'up-to-date':
      lines.push(`${name}: up to date${edits}${p.writeState ? `; ${would('recorded', 'would record')} the ki-diagram snapshot` : ''}`)
      break
    case 'update':
      lines.push(`${name}: ${would('applied', 'would apply')} ${p.applied.length} spec change(s)${edits}`)
      list('spec:', p.applied, (c) => `${c.id} (${c.change})`)
      break
    case 'conflict':
      lines.push(`${name}: ${p.conflicts.length} conflict(s) between hand edits and the spec; nothing written`)
      list('conflict:', p.conflicts, (c) => `${c.id} ${c.field}: drawing ${c.drawing}, spec ${c.spec}`)
      lines.push('  change the spec or the drawing so they agree, or rerun with --prefer drawing or --prefer spec')
      break
    case 'no-base':
      lines.push(`${name}: no record of what ki-diagram last wrote here, and the drawing differs from the spec in ${p.differing.length} record(s); nothing written`)
      list('differs:', p.differing, (id) => id)
      lines.push('  --adopt keeps the drawing as it is and treats every difference as a hand edit;')
      lines.push('  --base <file.md> names the drawing ki-diagram (or the old build script) last wrote')
      break
  }
  if (p.status !== 'conflict' && p.status !== 'no-base') {
    list('hand edit kept:', p.kept, (c) => `${c.id} (${c.change})`)
    list('conflict resolved:', p.conflicts.filter((c) => c.resolved), (c) => `${c.id} ${c.field}: took the ${c.resolved}'s ${c.resolved === 'spec' ? c.spec : c.drawing}`)
  }
  return lines
}

export const failed = (p) => p.status === 'conflict' || p.status === 'no-base'
