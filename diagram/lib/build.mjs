// Turns a checked spec into tldraw records, one page per spec page. The look
// follows the hand-drawn tldraw examples: draw font, white boxes with clean
// outlines, dashed light-red groups labelled underneath, plain labelled lines,
// a soft grey backdrop. Every number here matches the first generated
// drawings, so rebuilding them from their specs changes nothing but z-order keys.
import { color, pageId } from './spec.mjs'
import { tl, validated } from './tldraw.mjs'

const GROUP = 'light-red'
const W = 230
const H = 110

export function buildRecords(spec) {
  const records = [tl.DocumentRecordType.create({ id: tl.TLDOCUMENT_ID, name: spec.title })]
  spec.pages.forEach((page, i) => records.push(...buildPage(page, indexKey(i + 2))))
  return validated(records)
}

function buildPage(spec, index) {
  const page = tl.PageRecordType.create({ id: pageId(spec.name), name: spec.name, index, meta: {} })
  const out = [page]
  let next = 2 // the lowest key is kept for the backdrop, drawn under everything
  const add = (r) => { out.push(r); return r }
  const shape = (type, id, x, y, props, idx) => add({
    id: tl.createShapeId(`${page.id.slice(5)}-${id}`), typeName: 'shape', type, x, y, rotation: 0,
    index: idx || indexKey(++next), parentId: page.id, isLocked: false, opacity: 1, meta: {}, props,
  })
  const text = (id, x, y, w, value, textColor, size, align = 'middle') =>
    shape('text', id, x, y, { color: textColor, size, font: 'draw', textAlign: align, w, richText: tl.toRichText(value), scale: 1, autoSize: false })

  const ends = endpoints(spec.shapes)
  for (const s of spec.shapes) {
    if ('box' in s) shape('geo', s.box, s.x, s.y, geo(s.w || W, s.h || H, s.label ?? '', color(s.color), s))
    if ('stack' in s) {
      // Several of the same thing: two outlines peeking out behind the front box.
      for (const k of [2, 1]) shape('geo', `${s.stack}-back${k}`, s.x + 14 * k, s.y - 14 * k, geo(s.w || W, s.h || H, '', color(s.color), s))
      shape('geo', s.stack, s.x, s.y, geo(s.w || W, s.h || H, s.label ?? '', color(s.color), s))
    }
    if ('group' in s) {
      shape('geo', s.group, s.x, s.y, geo(s.w, s.h, '', GROUP, { dash: 'dashed', fill: 'none', size: 's' }))
      text(`${s.group}-label`, s.x, s.y + s.h + 10, s.w, s.label, GROUP, 'm')
    }
    if ('caption' in s) text(s.caption, s.x, s.y, s.w, s.text, 'grey', 's', s.align)
    if ('line' in s) line(s, ends, shape, add, page.id.slice(5))
  }
  placeIntroAndOutro(spec, out, shape)
  if (spec.backdrop !== false) backdrop(out, shape, indexKey(2))
  return out
}

const DIGITS = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'

// The n-th z-order key: a0..az, then b00..bzz. tldraw's own generator adds a
// random suffix, which would make every build differ from the last.
export function indexKey(n) {
  if (n < DIGITS.length) return `a${DIGITS[n]}`
  const m = n - DIGITS.length
  return `b${DIGITS[Math.floor(m / DIGITS.length)]}${DIGITS[m % DIGITS.length]}`
}

function geo(w, h, label, fillColor, o) {
  return {
    geo: o.geo || 'rectangle', dash: o.dash || 'draw', url: '', w, h, growY: 0, scale: 1, flipX: false, flipY: false,
    labelColor: o.label_color || 'black', color: fillColor, fill: o.fill || 'semi', size: o.size || 'm', font: 'draw',
    align: 'middle', verticalAlign: 'middle', richText: tl.toRichText(label),
  }
}

// Where each box, stack and group sits, so a line can name one declared later.
function endpoints(shapes) {
  const ends = new Map()
  for (const s of shapes) {
    const id = s.box ?? s.stack ?? s.group
    if (id !== undefined) ends.set(id, { x: s.x, y: s.y, w: s.w || W, h: s.h || H })
  }
  return ends
}

// A straight or bent line between two centres, bound at both ends so it
// follows the shapes when they are moved by hand.
function line(s, ends, shape, add, pageSlug) {
  const a = ends.get(s.from)
  const b = ends.get(s.to)
  const ca = { x: a.x + a.w / 2, y: a.y + a.h / 2 }
  const cb = { x: b.x + b.w / 2, y: b.y + b.h / 2 }
  const arrow = shape('arrow', `line-${s.line}`, ca.x, ca.y, {
    kind: 'arc', labelColor: 'black', color: color(s.color), fill: 'none', dash: s.dash || 'draw', size: 's',
    arrowheadStart: 'none', arrowheadEnd: s.head ? 'arrow' : 'none', font: 'draw',
    start: { x: 0, y: 0 }, end: { x: cb.x - ca.x, y: cb.y - ca.y }, bend: s.bend || 0,
    richText: tl.toRichText(s.label ?? ''), labelPosition: s.label_position ?? 0.5, scale: 1, elbowMidPoint: 0.5,
  })
  for (const [terminal, target] of [['start', s.from], ['end', s.to]]) {
    add({
      id: tl.createBindingId(`${arrow.id.slice(6)}-${terminal}`), typeName: 'binding', type: 'arrow', fromId: arrow.id,
      toId: tl.createShapeId(`${pageSlug}-${target}`), meta: {},
      props: { terminal, normalizedAnchor: { x: 0.5, y: 0.5 }, isExact: false, isPrecise: false, snap: 'none' },
    })
  }
}

const placed = (out) => out.filter((r) => r.typeName === 'shape' && r.type !== 'arrow')
const texty = (x, y, w, value, textColor, size) => [x, y, { color: textColor, size, font: 'draw', textAlign: 'start', w, richText: tl.toRichText(value), scale: 1, autoSize: false }]

// The intro (title and a line of context) goes above the content and the
// outro (notes for presenting the page) under it, once the content is known.
function placeIntroAndOutro(spec, out, shape) {
  if (spec.outro) {
    const shown = placed(out)
    const left = Math.min(...shown.map((r) => r.x))
    const bottom = Math.max(...shown.map((r) => r.y + (r.props.h || 60)))
    shape('text', 'outro-title', ...texty(left, bottom + 110, 1400, spec.outro.title, 'black', 'm'))
    shape('text', 'outro-body', ...texty(left, bottom + 170, 1400, spec.outro.body, 'black', 's'))
  }
  if (spec.intro) {
    const shown = placed(out)
    const left = Math.min(...shown.map((r) => r.x))
    const top = Math.min(...shown.map((r) => r.y))
    const right = Math.max(...shown.map((r) => r.x + (r.props.w || 0)))
    const width = Math.max(900, Math.min(right - left, 1400))
    shape('text', 'intro-title', ...texty(left, top - 200, width, spec.intro.title, 'black', 'l'))
    shape('text', 'intro-about', ...texty(left, top - 135, width, spec.intro.about, 'grey', 's'))
  }
}

// Soft grey panel behind the whole page, sized to its content. Text shapes
// carry no height; presenting notes under a page run to about ten lines.
function backdrop(out, shape, index) {
  const geos = placed(out)
  const minX = Math.min(...geos.map((s) => s.x)) - 70
  const minY = Math.min(...geos.map((s) => s.y)) - 70
  const maxX = Math.max(...geos.map((s) => s.x + (s.props.w || 0))) + 70
  const maxY = Math.max(...geos.map((s) => s.y + (s.props.h || (s.id.endsWith('outro-body') ? 360 : 40)))) + 70
  shape('geo', 'backdrop', minX, minY, geo(maxX - minX, maxY - minY, '', 'grey', { dash: 'none', fill: 'solid', size: 's' }), index)
}
