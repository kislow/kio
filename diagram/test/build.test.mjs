import assert from 'node:assert/strict'
import { test } from 'node:test'
import { validateIndexKey } from '@tldraw/utils'
import { buildRecords, indexKey } from '../lib/build.mjs'
import { serializedSchema } from '../lib/tldraw.mjs'
import { SPECS, golden, readSpec, stacking, withoutIndex } from './helpers.mjs'

// The goldens are the drawings the prototype build scripts last wrote. tldraw
// gave those random z-order keys, so the comparison is every other field of
// every record, plus the stacking order on every page.
for (const name of SPECS) {
  test(`${name} rebuilds the drawing the old build script wrote`, () => {
    const want = golden(name)
    const got = buildRecords(readSpec(name))
    assert.deepEqual(serializedSchema(), want.schema)
    assert.deepEqual(withoutIndex(got), withoutIndex(want.records))
    assert.deepEqual(stacking(got), stacking(want.records))
  })
}

test('builds are deterministic', () => {
  const spec = readSpec('kio-architecture')
  assert.deepEqual(buildRecords(spec), buildRecords(spec))
})

test('index keys are valid and strictly increasing', () => {
  let previous = ''
  for (let n = 0; n < 3906; n++) {
    const k = indexKey(n)
    validateIndexKey(k)
    assert.ok(k > previous, `${k} after ${previous}`)
    previous = k
  }
})

test('a line can name a box declared after it, and backdrop can be turned off', () => {
  const records = buildRecords({
    title: 'forward', pages: [{ name: 'P', backdrop: false, shapes: [{ line: 'l', from: 'a', to: 'b', head: true }, { box: 'a', x: 0, y: 0 }, { box: 'b', x: 400, y: 0, w: 100, h: 50 }] }],
  })
  const arrow = records.find((r) => r.id === 'shape:p-line-l')
  assert.deepEqual([arrow.x, arrow.y, arrow.props.end], [115, 55, { x: 335, y: -30 }])
  assert.equal(arrow.props.arrowheadEnd, 'arrow')
  assert.deepEqual(records.filter((r) => r.typeName === 'binding').map((r) => r.toId).sort(), ['shape:p-a', 'shape:p-b'])
  assert.equal(records.find((r) => r.id === 'shape:p-backdrop'), undefined)
})
