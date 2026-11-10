import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'
import { formatDrawing, frontmatter, readDrawing } from '../lib/drawing.mjs'
import { describe } from '../lib/merge.mjs'
import { apply, describePlan, drawingFor, failed, plan } from '../lib/plan.mjs'
import { statePath } from '../lib/state.mjs'
import { ROOT, golden, readSpec, specFile, tempDir, writeSpec } from './helpers.mjs'

const ARCH = 'kio-architecture'
const UUID = '00000000-0000-4000-8000-000000000000'

function drawingFromGolden(path, name, edit = (r) => r) {
  const g = golden(name)
  const records = g.records.map((r) => edit(structuredClone(r))).filter(Boolean)
  writeFileSync(path, formatDrawing({ before: frontmatter(['diagram']) + '<!-- kept -->\n', after: '\n%% trailing note %%\n', meta: { uuid: UUID }, schema: g.schema, records }))
}

// What an Obsidian session did to the architecture drawing: two labels edited
// by hand, the plugin's user record, a new pixel ratio, and arrows restacked.
function obsidianSession(r) {
  if (r.id === 'shape:overview-phone') r.props.richText = { type: 'doc', attrs: { dir: 'auto' }, content: [{ type: 'paragraph', attrs: { dir: 'auto' }, content: [{ type: 'text', text: 'Laptop/Phone' }] }] }
  if (r.id === 'shape:network-path-line-d-n') r.props.richText.content[0].content[0].text = 'tailnet'
  if (r.typeName === 'instance') r.devicePixelRatio = 2
  if (r.typeName === 'shape' && r.type === 'arrow') r.index = r.index.slice(0, 2) + 'Zz'
  return r
}

const label = (path, id) => describe(readDrawing(path).raw.records.find((r) => r.id === id)?.props.richText)

function editedSpec(dir, name, edit) {
  const spec = readSpec(name)
  edit(spec)
  return writeSpec(dir, name, spec)
}

const setLabel = (spec, pageName, id, text) => {
  const shape = spec.pages.find((p) => p.name === pageName).shapes.find((s) => (s.box ?? s.stack) === id)
  shape.label = text
}

test('a new drawing is written with its snapshot, then left alone', (t) => {
  const dir = tempDir(t)
  const specPath = join(dir, 'vault-pusher.diagram.yaml')
  copyFileSync(specFile('vault-pusher'), specPath)
  const first = plan({ specPath })
  assert.equal(first.status, 'new')
  apply(first)
  const text = readFileSync(join(dir, 'vault-pusher.md'), 'utf8')
  assert.match(text, /^---\ntldraw-file: true\ntags:\n {2}- diagram\n/)
  assert.ok(existsSync(statePath(join(dir, 'vault-pusher.md'))))
  const second = plan({ specPath })
  assert.equal(second.status, 'up-to-date')
  assert.equal(second.writeState, false)
  assert.deepEqual(describePlan(second), [`${join(dir, 'vault-pusher.md')}: up to date`])
})

test('hand edits from Obsidian survive a rebuild that changes other shapes', (t) => {
  const dir = tempDir(t)
  const drawing = join(dir, 'arch.md')
  const base = join(dir, 'last-written.md')
  drawingFromGolden(base, ARCH)
  drawingFromGolden(drawing, ARCH, obsidianSession)
  const before = readFileSync(drawing, 'utf8')

  // No snapshot yet and the drawing differs: refuse rather than guess.
  const refused = plan({ specPath: specFile(ARCH), drawingPath: drawing })
  assert.equal(refused.status, 'no-base')
  assert.deepEqual(refused.differing.sort(), ['shape:network-path-line-d-n', 'shape:overview-phone'])
  assert.ok(failed(refused))

  // Name what the old build script last wrote: nothing to change, two edits kept.
  const adopted = plan({ specPath: specFile(ARCH), drawingPath: drawing, basePath: base })
  assert.equal(adopted.status, 'up-to-date')
  assert.deepEqual(adopted.kept.map((k) => k.id).sort(), ['shape:network-path-line-d-n', 'shape:overview-phone'])
  apply(adopted)
  assert.equal(readFileSync(drawing, 'utf8'), before, 'adopting must not touch the drawing')

  // The spec changes another label: it lands, the hand edits stay, and so does
  // everything Obsidian keeps in the file.
  const specPath = editedSpec(dir, ARCH, (s) => setLabel(s, 'Overview', 'worker', 'kb-worker\n(Temporal)'))
  const update = plan({ specPath, drawingPath: drawing })
  assert.equal(update.status, 'update')
  assert.deepEqual(update.applied.map((a) => a.id), ['shape:overview-worker'])
  apply(update)
  assert.equal(label(drawing, 'shape:overview-worker'), '"kb-worker\\n(Temporal)"')
  assert.equal(label(drawing, 'shape:overview-phone'), '"Laptop/Phone"')
  assert.equal(label(drawing, 'shape:network-path-line-d-n'), '"tailnet"')
  const after = readDrawing(drawing)
  assert.equal(after.meta.uuid, UUID)
  assert.match(after.before, /<!-- kept -->\n$/)
  assert.equal(after.after, '\n%% trailing note %%\n')
  assert.equal(after.raw.records.find((r) => r.typeName === 'instance').devicePixelRatio, 2)
  assert.ok(after.raw.records.filter((r) => r.typeName === 'shape' && r.type === 'arrow').every((r) => r.index.endsWith('Zz')), 'arrow z-order keys stay as Obsidian left them')

  // And the next run has nothing to do.
  assert.equal(plan({ specPath, drawingPath: drawing }).status, 'up-to-date')
})

test('--adopt treats every difference as a hand edit', (t) => {
  const dir = tempDir(t)
  const drawing = join(dir, 'arch.md')
  drawingFromGolden(drawing, ARCH, obsidianSession)
  const p = plan({ specPath: specFile(ARCH), drawingPath: drawing, adopt: true })
  assert.equal(p.status, 'up-to-date')
  assert.equal(p.kept.length, 2)
  assert.ok(p.writeState)
})

test('a spec change to a hand-edited label is a conflict, and nothing is written', (t) => {
  const dir = tempDir(t)
  const drawing = join(dir, 'arch.md')
  drawingFromGolden(drawing, ARCH, obsidianSession)
  apply(plan({ specPath: specFile(ARCH), drawingPath: drawing, adopt: true }))
  const before = readFileSync(drawing, 'utf8')
  const state = readFileSync(statePath(drawing), 'utf8')
  const specPath = editedSpec(dir, ARCH, (s) => setLabel(s, 'Overview', 'phone', 'Phones'))

  const p = plan({ specPath, drawingPath: drawing })
  assert.equal(p.status, 'conflict')
  assert.deepEqual(describePlan(p).slice(0, 2), [
    `${drawing}: 1 conflict(s) between hand edits and the spec; nothing written`,
    '  conflict: shape:overview-phone props.richText: drawing "Laptop/Phone", spec "Phones"',
  ])
  assert.equal(readFileSync(drawing, 'utf8'), before)
  assert.equal(readFileSync(statePath(drawing), 'utf8'), state)

  const keep = plan({ specPath, drawingPath: drawing, prefer: 'drawing' })
  assert.equal(keep.status, 'up-to-date')
  assert.ok(describePlan(keep).includes(`  conflict resolved: shape:overview-phone props.richText: took the drawing's "Laptop/Phone"`))
  apply(keep)
  assert.equal(label(drawing, 'shape:overview-phone'), '"Laptop/Phone"')
  // The snapshot now holds the new spec, so the edit is still a hand edit and the conflict is gone.
  assert.equal(plan({ specPath, drawingPath: drawing }).status, 'up-to-date')

  const take = plan({ specPath: editedSpec(dir, ARCH, (s) => setLabel(s, 'Overview', 'phone', 'Phone, tablet')), drawingPath: drawing, prefer: 'spec' })
  apply(take)
  assert.equal(label(drawing, 'shape:overview-phone'), '"Phone, tablet"')
})

test('a drawing saved while the merge ran is not overwritten', (t) => {
  const dir = tempDir(t)
  const drawing = join(dir, 'arch.md')
  drawingFromGolden(drawing, ARCH)
  apply(plan({ specPath: specFile(ARCH), drawingPath: drawing, adopt: true }))
  const p = plan({ specPath: editedSpec(dir, ARCH, (s) => setLabel(s, 'Overview', 'worker', 'worker')), drawingPath: drawing })
  writeFileSync(drawing, readFileSync(drawing, 'utf8') + '\n')
  assert.throws(() => apply(p), /changed while it was being merged/)
})

test('a drawing saved by a newer tldraw is refused', (t) => {
  const dir = tempDir(t)
  const drawing = join(dir, 'arch.md')
  const g = golden('vault-pusher')
  const schema = structuredClone(g.schema)
  schema.sequences['com.tldraw.shape.geo'] += 1
  writeFileSync(drawing, formatDrawing({ before: frontmatter([]), meta: {}, schema, records: g.records }))
  assert.throws(() => plan({ specPath: specFile('vault-pusher'), drawingPath: drawing }), /saved by a newer tldraw/)
})

test('drawingFor needs the .diagram.yaml naming', () => {
  assert.equal(drawingFor('/v/ki CLI.diagram.yaml'), '/v/ki CLI.md')
  assert.equal(drawingFor('/v/.ki-diagram/ki CLI.diagram.yaml'), '/v/ki CLI.md')
  assert.equal(drawingFor('x.diagram.json'), 'x.md')
  assert.throws(() => drawingFor('x.yaml'), /--drawing/)
})

const cli = (...args) => spawnSync(process.execPath, [join(ROOT, 'bin', 'ki-diagram.mjs'), ...args], { encoding: 'utf8' })

test('the command line', async (t) => {
  const dir = tempDir(t)
  // The layout the vault uses: spec and snapshot hidden, the drawing beside them.
  mkdirSync(join(dir, '.ki-diagram'))
  const specPath = join(dir, '.ki-diagram', 'vault-pusher.diagram.yaml')
  copyFileSync(specFile('vault-pusher'), specPath)
  const bad = writeSpec(dir, 'bad', { title: 't', pages: [{ name: 'P', shapes: [{ box: 'a' }] }] })
  const cases = [
    { name: 'help', args: ['help'], code: 0, out: /usage:/ },
    { name: 'unknown command', args: ['draw'], code: 2, err: /unknown command draw/ },
    { name: 'unknown option', args: ['build', specPath, '--force'], code: 2, err: /unknown option --force/ },
    { name: 'bad --prefer', args: ['build', specPath, '--prefer', 'mine'], code: 2, err: /--prefer is drawing or spec/ },
    { name: 'adopt and base together', args: ['build', specPath, '--adopt', '--base', 'x.md'], code: 2, err: /alternatives/ },
    { name: 'dry run writes nothing', args: ['build', specPath, '--dry-run'], code: 0, out: /would write a new drawing/, absent: join(dir, 'vault-pusher.md') },
    { name: 'build', args: ['build', specPath], code: 0, out: /vault-pusher\.md: wrote a new drawing, 2 page\(s\), 51 shapes/, present: [join(dir, 'vault-pusher.md'), join(dir, '.ki-diagram', 'vault-pusher.base.json')] },
    { name: 'check passes and fails per spec', args: ['check', specPath, bad], code: 1, out: /up to date[\s\S]*x must be a number/ },
    { name: 'requirements as JSON', args: ['requirements', '--json'], out: /"name":"packages","ok":true/ },
  ]
  for (const c of cases) {
    await t.test(c.name, () => {
      const r = cli(...c.args)
      if (c.code !== undefined) assert.equal(r.status, c.code, r.stderr + r.stdout)
      if (c.out) assert.match(r.stdout, c.out)
      if (c.err) assert.match(r.stderr, c.err)
      if (c.absent) assert.ok(!existsSync(c.absent))
      for (const p of c.present ?? []) assert.ok(existsSync(p), p)
    })
  }
})
