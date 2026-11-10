// Loads a drawing spec (YAML or JSON) and checks all of it, collecting every
// problem before failing, so one run lists everything to fix.
import { readFileSync } from 'node:fs'
import { parse } from 'yaml'
import { tl } from './tldraw.mjs'

// Colour carries meaning, the same on every page: core harness parts, the path
// an agent's request takes, and services the harness runs on.
export const ROLES = { core: 'black', path: 'blue', external: 'green' }

// Limits of the z-order keys the builder hands out (indexKey in build.mjs).
export const MAX_PAGES = 60
export const MAX_SHAPES = 3800

const KINDS = ['box', 'stack', 'group', 'caption', 'line']
const ID = /^[a-z0-9][a-z0-9-]*$/i
const COLORS = tl.DefaultColorStyle.values

const KEYS = {
  spec: ['title', 'tags', 'pages'],
  page: ['name', 'intro', 'outro', 'backdrop', 'shapes'],
  intro: ['title', 'about'],
  outro: ['title', 'body'],
  box: ['box', 'x', 'y', 'w', 'h', 'label', 'color', 'geo', 'dash', 'fill', 'size', 'label_color'],
  stack: ['stack', 'x', 'y', 'w', 'h', 'label', 'color', 'geo', 'dash', 'fill', 'size', 'label_color'],
  group: ['group', 'x', 'y', 'w', 'h', 'label'],
  caption: ['caption', 'x', 'y', 'w', 'text', 'align'],
  line: ['line', 'from', 'to', 'label', 'color', 'dash', 'head', 'bend', 'label_position'],
}

export function loadSpec(path) {
  let text
  try {
    text = readFileSync(path, 'utf8')
  } catch (err) {
    throw new SpecError(path, [`cannot read it: ${err.message}`])
  }
  let data
  try {
    data = parse(text)
  } catch (err) {
    throw new SpecError(path, [`not valid YAML: ${err.message}`])
  }
  const problems = checkSpec(data)
  if (problems.length) throw new SpecError(path, problems)
  return data
}

export class SpecError extends Error {
  constructor(path, problems) {
    super(`${path}: ${problems.length} problem(s)\n` + problems.map((p) => `  ${p}`).join('\n'))
    this.problems = problems
  }
}

export const color = (value) => ROLES[value ?? 'core'] ?? value

const slug = (name) => name.toLowerCase().replace(/[^a-z0-9]+/g, '-')
export const pageId = (name) => `page:${slug(name)}`

// Returns every problem found; an empty list means the spec can be built.
export function checkSpec(spec) {
  const problems = []
  const at = (where) => (msg) => problems.push(`${where}: ${msg}`)
  if (!isObject(spec)) return ['the spec must be a mapping with title, tags and pages']
  unknownKeys(spec, KEYS.spec, at('spec'))
  if (!nonEmptyString(spec.title)) at('spec')('title must be a non-empty string')
  if (spec.tags !== undefined && !(Array.isArray(spec.tags) && spec.tags.every(nonEmptyString))) at('spec')('tags must be a list of strings')
  if (!Array.isArray(spec.pages) || spec.pages.length === 0) {
    at('spec')('pages must be a non-empty list')
    return problems
  }
  if (spec.pages.length > MAX_PAGES) at('spec')(`at most ${MAX_PAGES} pages; split the drawing`)
  const pageIds = new Map()
  spec.pages.forEach((page, i) => {
    const where = `pages[${i}]${isObject(page) && nonEmptyString(page.name) ? ` (${page.name})` : ''}`
    if (!isObject(page)) return at(where)('must be a mapping')
    unknownKeys(page, KEYS.page, at(where))
    if (!nonEmptyString(page.name)) at(where)('name must be a non-empty string')
    else if (pageIds.has(pageId(page.name))) at(where)(`name gives the same page id as pages[${pageIds.get(pageId(page.name))}]`)
    else pageIds.set(pageId(page.name), i)
    for (const part of ['intro', 'outro']) {
      if (page[part] === undefined) continue
      if (!isObject(page[part])) { at(where)(`${part} must be a mapping`); continue }
      unknownKeys(page[part], KEYS[part], at(`${where}.${part}`))
      for (const k of KEYS[part]) if (!nonEmptyString(page[part][k])) at(`${where}.${part}`)(`${k} must be a non-empty string`)
    }
    if (page.backdrop !== undefined && typeof page.backdrop !== 'boolean') at(where)('backdrop must be true or false')
    checkShapes(page.shapes, where, at)
  })
  return problems
}

function checkShapes(shapes, where, at) {
  if (!Array.isArray(shapes) || shapes.length === 0) return at(where)('shapes must be a non-empty list')
  if (shapes.length > MAX_SHAPES) at(where)(`at most ${MAX_SHAPES} shapes on a page; split it`)
  const ids = new Map() // final shape id suffix -> where it was declared
  const ends = new Set()
  const lines = []
  shapes.forEach((s, j) => {
    const kinds = isObject(s) ? KINDS.filter((k) => k in s) : []
    const w = `${where}.shapes[${j}]`
    if (kinds.length !== 1) return at(w)(`must have exactly one of ${KINDS.join(', ')}`)
    const kind = kinds[0]
    const id = s[kind]
    const here = at(`${w} (${kind} ${id})`)
    unknownKeys(s, KEYS[kind], here)
    if (typeof id !== 'string' || !ID.test(id)) return here('id must be letters, digits and dashes')
    const own = kind === 'line' ? [`line-${id}`] : kind === 'group' ? [id, `${id}-label`] : kind === 'stack' ? [id, `${id}-back1`, `${id}-back2`] : [id]
    for (const o of own) {
      if (ids.has(o)) here(`id clashes with ${ids.get(o)}`)
      else ids.set(o, `${kind} ${id}`)
    }
    if (kind === 'line') return lines.push({ s, here })
    for (const k of ['x', 'y']) if (!isNumber(s[k])) here(`${k} must be a number`)
    if (kind !== 'caption') ends.add(id)
    if (kind === 'group' || kind === 'caption') {
      if (!isPositive(s.w)) here('w must be a positive number')
    } else if (s.w !== undefined && !isPositive(s.w)) here('w must be a positive number')
    if (kind === 'group' && !isPositive(s.h)) here('h must be a positive number')
    if ((kind === 'box' || kind === 'stack') && s.h !== undefined && !isPositive(s.h)) here('h must be a positive number')
    if (kind === 'caption' && !nonEmptyString(s.text)) here('text must be a non-empty string')
    if (kind !== 'caption' && s.label !== undefined && typeof s.label !== 'string') here('label must be a string')
    if (kind === 'group' && !nonEmptyString(s.label)) here('a group needs a label')
    checkStyles(s, here)
  })
  for (const { s, here } of lines) {
    for (const end of ['from', 'to']) {
      if (typeof s[end] !== 'string') here(`${end} must name a box, stack or group`)
      else if (!ends.has(s[end])) here(`${end} "${s[end]}" is not a box, stack or group on this page`)
    }
    if (s.label !== undefined && typeof s.label !== 'string') here('label must be a string')
    if (s.head !== undefined && typeof s.head !== 'boolean') here('head must be true or false')
    if (s.bend !== undefined && !isNumber(s.bend)) here('bend must be a number')
    if (s.label_position !== undefined && !(isNumber(s.label_position) && s.label_position >= 0 && s.label_position <= 1)) here('label_position must be between 0 and 1')
    checkStyles(s, here)
  }
}

function checkStyles(s, here) {
  const allowed = {
    color: [...Object.keys(ROLES), ...COLORS],
    label_color: COLORS,
    geo: tl.GeoShapeGeoStyle.values,
    dash: tl.DefaultDashStyle.values,
    fill: tl.DefaultFillStyle.values,
    size: tl.DefaultSizeStyle.values,
    align: tl.DefaultTextAlignStyle.values,
  }
  for (const [k, values] of Object.entries(allowed)) {
    if (s[k] !== undefined && !values.includes(s[k])) here(`${k} "${s[k]}" is not one of ${values.join(', ')}`)
  }
}

function unknownKeys(obj, known, report) {
  for (const k of Object.keys(obj)) if (!known.includes(k)) report(`unknown key "${k}"`)
}

const isObject = (v) => v !== null && typeof v === 'object' && !Array.isArray(v)
const nonEmptyString = (v) => typeof v === 'string' && v.trim() !== ''
const isNumber = (v) => typeof v === 'number' && Number.isFinite(v)
const isPositive = (v) => isNumber(v) && v > 0

// Style warnings: the spec builds, but breaks a drawing convention.
export function lintSpec(spec) {
  const warnings = []
  spec.pages.forEach((page) => {
    if (!page.intro) warnings.push(`page "${page.name}" has no intro: start every page with a title and a line of context`)
  })
  return warnings
}
