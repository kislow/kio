import assert from 'node:assert/strict'
import { test } from 'node:test'
import { checkSpec, lintSpec } from '../lib/spec.mjs'
import { SPECS, readSpec } from './helpers.mjs'

const page = (shapes, extra = {}) => ({ title: 't', pages: [{ name: 'One', intro: { title: 'a', about: 'b' }, shapes, ...extra }] })

test('the ported specs are valid', () => {
  for (const name of SPECS) assert.deepEqual(checkSpec(readSpec(name)), [], name)
})

test('checkSpec reports every problem, not just the first', () => {
  const cases = [
    { name: 'not a mapping', spec: [], want: ['the spec must be a mapping with title, tags and pages'] },
    { name: 'no pages', spec: { title: 't', pages: [] }, want: ['spec: pages must be a non-empty list'] },
    {
      name: 'several shape problems at once',
      spec: page([
        { box: 'a', x: 0, y: 0, colour: 'red' },
        { box: 'a', x: 'left', y: 0 },
        { line: 'l', from: 'a', to: 'nowhere', label_position: 2 },
        { caption: 'c', x: 0, y: 0 },
        { group: 'g', x: 0, y: 0, w: 10, h: 10 },
        { box: 'b', caption: 'b', x: 0, y: 0 },
        { box: 'Bad id!', x: 0, y: 0 },
        { box: 'd', x: 0, y: 0, color: 'purple', geo: 'blob' },
      ]),
      want: [
        'pages[0] (One).shapes[0] (box a): unknown key "colour"',
        'pages[0] (One).shapes[1] (box a): id clashes with box a',
        'pages[0] (One).shapes[1] (box a): x must be a number',
        'pages[0] (One).shapes[3] (caption c): w must be a positive number',
        'pages[0] (One).shapes[3] (caption c): text must be a non-empty string',
        'pages[0] (One).shapes[4] (group g): a group needs a label',
        'pages[0] (One).shapes[5]: must have exactly one of box, stack, group, caption, line',
        'pages[0] (One).shapes[6] (box Bad id!): id must be letters, digits and dashes',
        'pages[0] (One).shapes[7] (box d): color "purple" is not one of core, path, external, black, grey, light-violet, violet, blue, light-blue, yellow, orange, green, light-green, light-red, red, white',
        'pages[0] (One).shapes[7] (box d): geo "blob" is not one of cloud, rectangle, ellipse, triangle, diamond, pentagon, hexagon, octagon, star, rhombus, rhombus-2, oval, trapezoid, arrow-right, arrow-left, arrow-up, arrow-down, x-box, check-box, heart',
        'pages[0] (One).shapes[2] (line l): to "nowhere" is not a box, stack or group on this page',
        'pages[0] (One).shapes[2] (line l): label_position must be between 0 and 1',
      ],
    },
    {
      name: 'stack and group ids own their helper shapes',
      spec: page([{ stack: 's', x: 0, y: 0 }, { box: 's-back1', x: 0, y: 0 }, { group: 'g', x: 0, y: 0, w: 1, h: 1, label: 'G' }, { caption: 'g-label', x: 0, y: 0, w: 1, text: 'x' }]),
      want: ['pages[0] (One).shapes[1] (box s-back1): id clashes with stack s', 'pages[0] (One).shapes[3] (caption g-label): id clashes with group g'],
    },
    {
      name: 'page names must give distinct ids',
      spec: { title: 't', pages: [{ name: 'A b', shapes: [{ box: 'x', x: 0, y: 0 }] }, { name: 'a-B', shapes: [{ box: 'x', x: 0, y: 0 }] }] },
      want: ['pages[1] (a-B): name gives the same page id as pages[0]'],
    },
  ]
  for (const c of cases) assert.deepEqual(checkSpec(c.spec), c.want, c.name)
})

test('lintSpec asks for an intro on every page', () => {
  const spec = { title: 't', pages: [{ name: 'Bare', shapes: [{ box: 'a', x: 0, y: 0 }] }] }
  assert.deepEqual(lintSpec(spec), ['page "Bare" has no intro: start every page with a title and a line of context'])
  assert.deepEqual(lintSpec(readSpec('ki-cli')), [])
})
