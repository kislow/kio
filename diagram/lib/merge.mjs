// Three-way merge of a drawing's records, keyed by record id and then by field:
//   base:    what ki-diagram last wrote (its snapshot in .ki-diagram/)
//   drawing: the file as it is now, with any hand edits made in Obsidian
//   spec:    what the spec builds today
// A field the spec did not change keeps the drawing's value, so hand edits
// survive; a field only the spec changed takes the spec's value; a field both
// changed differently is a conflict, and nothing is written unless the caller
// says which side wins.
import { isDeepStrictEqual } from 'node:util'

// Z-order keys belong to the drawing once a shape exists: tldraw hands out
// random keys and restacks arrows on load, and a hand restack is an edit, so
// `index` is never compared or overwritten on a record the drawing already has.
const OWNED_BY_DRAWING = new Set(['index'])

export function mergeRecords({ base, drawing, spec, prefer }) {
  const b = byId(base)
  const t = byId(drawing)
  const o = byId(spec)
  const result = new Map()
  const report = { applied: [], kept: [], conflicts: [] }
  for (const id of new Set([...t.keys(), ...o.keys(), ...b.keys()])) {
    const merged = mergeRecord(id, b.get(id), t.get(id), o.get(id), prefer, report)
    if (merged) result.set(id, merged)
  }
  checkIntegrity(result, t, report)
  // Keep the drawing's record order so a rewrite changes as few lines as possible.
  const order = [...t.keys(), ...o.keys()].filter((id, i, all) => all.indexOf(id) === i && result.has(id))
  return { records: placeNewShapes(order.map((id) => result.get(id)), t), ...report }
}

function mergeRecord(id, b, t, o, prefer, report) {
  if (same(t, o)) return t
  if (same(b, t)) {
    report.applied.push({ id, change: !o ? 'removed' : !t ? 'added' : fieldsChanged(t, o).join(', ') })
    return o && t ? keepOwned(o, t) : o
  }
  if (same(b, o)) {
    report.kept.push({ id, change: !t ? 'deleted by hand' : !b ? 'added by hand' : fieldsChanged(b, t).join(', ') })
    return t
  }
  if (!t || !o) {
    const conflict = { id, field: '(record)', drawing: t ? 'changed' : 'deleted', spec: o ? 'changed' : 'removed' }
    return resolve(conflict, t, o, prefer, report)
  }
  return mergeFields(id, b ?? {}, t, o, prefer, report)
}

// Both sides changed the record: merge field by field (top-level fields, and
// each prop on its own), so a box moved by hand still takes a new label.
function mergeFields(id, b, t, o, prefer, report) {
  const out = { ...t, props: t.props && { ...t.props } }
  const kept = []
  const applied = []
  for (const [path, get, set] of fields(b, t, o)) {
    const [fb, ft, fo] = [get(b), get(t), get(o)]
    if (OWNED_BY_DRAWING.has(path) || same(ft, fo)) continue
    if (same(fb, fo)) {
      kept.push(path)
      continue
    }
    if (same(fb, ft)) {
      set(out, fo)
      applied.push(path)
      continue
    }
    const conflict = { id, field: path, drawing: describe(ft), spec: describe(fo) }
    if (prefer === 'spec') set(out, fo)
    if (prefer) conflict.resolved = prefer
    report.conflicts.push(conflict)
  }
  if (applied.length) report.applied.push({ id, change: applied.join(', ') })
  if (kept.length) report.kept.push({ id, change: kept.join(', ') })
  return out
}

function resolve(conflict, t, o, prefer, report) {
  if (prefer) conflict.resolved = prefer
  report.conflicts.push(conflict)
  return prefer === 'spec' ? o : t
}

// Every field path of a record with a getter and setter: props one by one,
// everything else as a whole.
function fields(...records) {
  const top = new Set()
  const props = new Set()
  for (const r of records) {
    for (const k of Object.keys(r ?? {})) (k === 'props' ? null : top.add(k))
    for (const k of Object.keys(r?.props ?? {})) props.add(k)
  }
  const del = (obj, k, v) => { if (v === undefined) delete obj[k]; else obj[k] = v }
  return [
    ...[...top].map((k) => [k, (r) => r?.[k], (r, v) => del(r, k, v)]),
    ...[...props].map((k) => [`props.${k}`, (r) => r?.props?.[k], (r, v) => del((r.props ??= {}), k, v)]),
  ]
}

const fieldsChanged = (a, b) => fields(a, b).filter(([path, get]) => !OWNED_BY_DRAWING.has(path) && !same(get(a), get(b))).map(([path]) => path)

function keepOwned(o, t) {
  const out = { ...o }
  for (const k of OWNED_BY_DRAWING) if (k in t) out[k] = t[k]
  return out
}

// Equal for merging purposes: z-order keys ignored, and tldraw's text
// direction marker (added whenever a label is edited) is not a change.
export function same(a, b) {
  if (a === undefined || b === undefined) return a === b
  return isDeepStrictEqual(normalise(a), normalise(b))
}

function normalise(v) {
  if (Array.isArray(v)) return v.map(normalise)
  if (v === null || typeof v !== 'object') return v
  const out = {}
  for (const [k, val] of Object.entries(v)) {
    if (OWNED_BY_DRAWING.has(k)) continue
    if (k === 'attrs' && val && typeof val === 'object' && val.dir === 'auto') {
      const { dir, ...rest } = val
      if (Object.keys(rest).length) out[k] = normalise(rest)
      continue
    }
    out[k] = normalise(val)
  }
  return out
}

// A binding must join two shapes that exist, and a shape must sit on a page or
// shape that exists; a merge that breaks either is a conflict to resolve by hand.
function checkIntegrity(result, drawing, report) {
  for (const r of result.values()) {
    const missing = r.typeName === 'binding'
      ? [r.fromId, r.toId].filter((id) => !result.has(id))
      : r.typeName === 'shape' && !result.has(r.parentId) ? [r.parentId] : []
    for (const id of missing) {
      report.conflicts.push({ id: r.id, field: '(link)', drawing: drawing.has(r.id) ? 'kept' : 'missing', spec: `points at ${id}, which the merge removes`, unresolvable: true })
    }
  }
}

// A shape new to the drawing takes the spec's z-order key unless that key is
// already used under the same parent; then it goes just above the clash.
function placeNewShapes(records, drawing) {
  const used = new Set(records.filter((r) => drawing.has(r.id) && 'index' in r).map((r) => `${r.parentId ?? r.typeName}|${r.index}`))
  return records.map((r) => {
    if (drawing.has(r.id) || !('index' in r)) return r
    let index = r.index
    while (used.has(`${r.parentId ?? r.typeName}|${index}`)) index += 'V'
    used.add(`${r.parentId ?? r.typeName}|${index}`)
    return index === r.index ? r : { ...r, index }
  })
}

const byId = (records) => new Map((records ?? []).map((r) => [r.id, r]))

// A short readable form of a field value for conflict messages.
export function describe(v) {
  if (v === undefined) return '(none)'
  const text = plainText(v)
  if (text !== null) return JSON.stringify(text)
  const s = JSON.stringify(v)
  return s.length > 60 ? s.slice(0, 57) + '...' : s
}

function plainText(v) {
  if (!v || v.type !== 'doc' || !Array.isArray(v.content)) return null
  const walk = (n) => (n.text ?? '') + (n.content ?? []).map(walk).join(n.type === 'doc' ? '\n' : '')
  return v.content.map(walk).join('\n')
}
