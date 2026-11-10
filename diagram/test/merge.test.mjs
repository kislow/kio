import assert from 'node:assert/strict'
import { test } from 'node:test'
import { describe, mergeRecords, same } from '../lib/merge.mjs'
import { tl } from '../lib/tldraw.mjs'

const box = (id, label, extra = {}) => ({
  id: `shape:${id}`, typeName: 'shape', type: 'geo', x: 0, y: 0, index: 'a3', parentId: 'page:p', ...extra,
  props: { w: 100, h: 50, richText: tl.toRichText(label), ...extra.props },
})
const page = { id: 'page:p', typeName: 'page', name: 'P', index: 'a2', meta: {} }
const binding = (id, to) => ({ id: `binding:${id}`, typeName: 'binding', type: 'arrow', fromId: 'shape:arrow', toId: `shape:${to}`, props: {} })
const label = (records, id) => describe(records.find((r) => r.id === `shape:${id}`)?.props.richText)
const edited = (text) => ({ type: 'doc', attrs: { dir: 'auto' }, content: [{ type: 'paragraph', attrs: { dir: 'auto' }, content: [{ type: 'text', text }] }] })

test('three-way merge', async (t) => {
  const cases = [
    {
      name: 'untouched drawing takes the spec change',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'old')], spec: [page, box('a', 'new')],
      labels: { a: '"new"' }, applied: 1, kept: 0, conflicts: 0,
    },
    {
      name: 'hand edit survives when the spec did not change',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'mine')], spec: [page, box('a', 'old')],
      labels: { a: '"mine"' }, applied: 0, kept: 1, conflicts: 0,
    },
    {
      name: 'a box moved by hand still takes the new label',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'old', { x: 300 })], spec: [page, box('a', 'new')],
      labels: { a: '"new"' }, x: { a: 300 }, applied: 1, kept: 1, conflicts: 0,
    },
    {
      name: 'both changed the label differently: conflict',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'mine')], spec: [page, box('a', 'new')],
      labels: { a: '"mine"' }, applied: 0, kept: 0, conflicts: 1,
    },
    {
      name: 'conflict resolved for the drawing',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'mine')], spec: [page, box('a', 'new')], prefer: 'drawing',
      labels: { a: '"mine"' }, conflicts: 1, resolved: 'drawing',
    },
    {
      name: 'conflict resolved for the spec',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'mine')], spec: [page, box('a', 'new')], prefer: 'spec',
      labels: { a: '"new"' }, conflicts: 1, resolved: 'spec',
    },
    {
      name: 'both made the same change',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'new')], spec: [page, box('a', 'new')],
      labels: { a: '"new"' }, applied: 0, kept: 0, conflicts: 0,
    },
    {
      name: 'a shape added by hand stays',
      base: [page], drawing: [page, box('mine', 'x')], spec: [page],
      labels: { mine: '"x"' }, applied: 0, kept: 1, conflicts: 0,
    },
    {
      name: 'a shape deleted by hand stays deleted',
      base: [page, box('a', 'old')], drawing: [page], spec: [page, box('a', 'old')],
      labels: { a: '(none)' }, applied: 0, kept: 1, conflicts: 0,
    },
    {
      name: 'deleted by hand but changed in the spec: conflict',
      base: [page, box('a', 'old')], drawing: [page], spec: [page, box('a', 'new')],
      labels: { a: '(none)' }, conflicts: 1,
    },
    {
      name: 'removed from the spec and untouched: removed',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'old')], spec: [page],
      labels: { a: '(none)' }, applied: 1, conflicts: 0,
    },
    {
      name: 'removed from the spec but edited by hand: conflict',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'mine')], spec: [page],
      conflicts: 1,
    },
    {
      name: 'a new z-order key from tldraw is not an edit, and is kept',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'old', { index: 'a9xYz' })], spec: [page, box('a', 'new')],
      labels: { a: '"new"' }, index: { a: 'a9xYz' }, applied: 1, kept: 0, conflicts: 0,
    },
    {
      name: "tldraw's text direction marker is not an edit",
      base: [page, box('a', 'old')], drawing: [page, box('a', 'old', { props: { richText: edited('old') } })], spec: [page, box('a', 'old')],
      applied: 0, kept: 0, conflicts: 0, unchanged: true,
    },
    {
      name: 'a new spec shape whose key is taken goes just above it',
      base: [page], drawing: [page, box('mine', 'x', { index: 'a3' })], spec: [page, box('a', 'new', { index: 'a3' })],
      index: { a: 'a3V', mine: 'a3' }, applied: 1, kept: 1, conflicts: 0,
    },
    {
      name: 'a hand-drawn arrow bound to a box the spec removes: conflict',
      base: [page, box('a', 'old')], drawing: [page, box('a', 'old'), box('arrow', ''), binding('b', 'a')], spec: [page],
      conflicts: 1, unresolvable: true,
    },
  ]
  for (const c of cases) {
    await t.test(c.name, () => {
      const got = mergeRecords({ base: c.base, drawing: c.drawing, spec: c.spec, prefer: c.prefer })
      for (const [id, want] of Object.entries(c.labels ?? {})) assert.equal(label(got.records, id), want, `label of ${id}`)
      for (const [id, want] of Object.entries(c.x ?? {})) assert.equal(got.records.find((r) => r.id === `shape:${id}`).x, want)
      for (const [id, want] of Object.entries(c.index ?? {})) assert.equal(got.records.find((r) => r.id === `shape:${id}`).index, want)
      if (c.applied !== undefined) assert.equal(got.applied.length, c.applied, 'applied')
      if (c.kept !== undefined) assert.equal(got.kept.length, c.kept, 'kept')
      assert.equal(got.conflicts.length, c.conflicts, 'conflicts')
      if (c.resolved) assert.equal(got.conflicts[0].resolved, c.resolved)
      if (c.unresolvable) assert.ok(got.conflicts[0].unresolvable)
      if (c.unchanged) assert.deepEqual(got.records, c.drawing)
    })
  }
})

test('same ignores z-order keys and text direction, nothing else', () => {
  assert.ok(same(box('a', 'x', { index: 'a1' }), box('a', 'x', { index: 'b00' })))
  assert.ok(same(box('a', 'x'), box('a', 'x', { props: { richText: edited('x') } })))
  assert.ok(!same(box('a', 'x'), box('a', 'y')))
  assert.ok(!same(box('a', 'x'), undefined))
})

test('describe shows labels as text', () => {
  assert.equal(describe(edited('Laptop/Phone')), '"Laptop/Phone"')
  assert.equal(describe(undefined), '(none)')
  assert.equal(describe(12), '12')
  assert.match(describe({ long: 'x'.repeat(80) }), /\.\.\.$/)
})
