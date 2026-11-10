import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'
import { apply, plan } from '../lib/plan.mjs'
import { browserCheck } from '../lib/requirements.mjs'
import { ROOT, specFile, tempDir } from './helpers.mjs'

// CI sets KI_DIAGRAM_REQUIRE_BROWSER=1 so a missing browser fails the run
// instead of skipping the one test that proves PDFs still render.
const browser = browserCheck()
const skip = !browser.ok && process.env.KI_DIAGRAM_REQUIRE_BROWSER !== '1' && `no browser: ${browser.detail}`

test('renders every page to its own PDF page, offline', { skip, timeout: 180_000 }, async (t) => {
  const { renderPdf } = await import('../lib/render.mjs')
  const dir = tempDir(t)
  const drawing = join(dir, 'ki-cli.md')
  apply(plan({ specPath: specFile('ki-cli'), drawingPath: drawing }))
  const pdf = join(dir, 'ki-cli.pdf')
  const pages = await renderPdf(drawing, pdf)
  assert.deepEqual(pages, ['Starting a session', 'Scope and guard', 'Commands', 'Vault push'])
  const bytes = readFileSync(pdf).toString('latin1')
  assert.ok(bytes.startsWith('%PDF-'))
  const boxes = [...bytes.matchAll(/\/MediaBox \[0 0 ([\d.]+) ([\d.]+)\]/g)].map((m) => `${Math.round(m[1])}x${Math.round(m[2])}`)
  assert.equal(boxes.length, 4)
  assert.ok(new Set(boxes).size > 1, `each page is sized to its drawing: ${boxes}`)
  assert.match(bytes, /ShantellSans/, 'the draw font is embedded, not fetched')
})

const cli = (...args) => execFileSync('node', [join(ROOT, 'bin', 'ki-diagram.mjs'), ...args], { encoding: 'utf8' })
const PAGES = ['Starting a session', 'Scope and guard', 'Commands', 'Vault push']

function drawingIn(dir) {
  const drawing = join(dir, 'ki-cli.md')
  apply(plan({ specPath: specFile('ki-cli'), drawingPath: drawing }))
  return drawing
}

test('renders one standalone SVG per page, replacing stale ones', { skip, timeout: 180_000 }, async (t) => {
  const { renderSvg } = await import('../lib/render.mjs')
  const dir = tempDir(t)
  const out = join(dir, 'svg', 'nested')
  mkdirSync(out, { recursive: true })
  writeFileSync(join(out, '07.svg'), 'stale')
  writeFileSync(join(out, 'notes.txt'), 'kept')
  const pages = await renderSvg(drawingIn(dir), out)
  assert.deepEqual(pages.map((p) => p.name), PAGES)
  assert.deepEqual(pages.map((p) => p.index), [1, 2, 3, 4])
  assert.deepEqual(readdirSync(out).sort(), ['01.svg', '02.svg', '03.svg', '04.svg', 'notes.txt'])
  for (const p of pages) {
    assert.equal(p.file, join(out, `${String(p.index).padStart(2, '0')}.svg`))
    const svg = readFileSync(p.file, 'utf8')
    assert.match(svg, /^<svg[^>]*xmlns="http:\/\/www.w3.org\/2000\/svg"/)
    assert.ok(p.width > 0 && p.height > 0)
  }
  assert.ok(new Set(pages.map((p) => `${p.width}x${p.height}`)).size > 1, 'each SVG is sized to its page')
  assert.match(readFileSync(pages[0].file, 'utf8'), /@font-face[^}]*data:font\/woff2/, 'fonts are embedded, not linked')
})

test('the theme switches tldraw dark mode and keeps the background', { skip, timeout: 180_000 }, async (t) => {
  const { renderSvg } = await import('../lib/render.mjs')
  const dir = tempDir(t)
  const drawing = drawingIn(dir)
  const [light] = await renderSvg(drawing, join(dir, 'light'))
  const [dark] = await renderSvg(drawing, join(dir, 'dark'), { theme: 'dark' })
  const lightSvg = readFileSync(light.file, 'utf8')
  const darkSvg = readFileSync(dark.file, 'utf8')
  const background = (svg) => svg.match(/^<svg[^>]*style="background-color: ([^;"]+)/)?.[1]
  assert.ok(background(lightSvg), 'light SVG carries a background')
  assert.ok(background(darkSvg), 'dark SVG carries a background')
  assert.notEqual(background(lightSvg), background(darkSvg))
  assert.match(darkSvg.slice(0, 400), /tl-theme__dark/)
})

test('render --svg prints the paths, or one JSON line with --json', { skip, timeout: 180_000 }, (t) => {
  const dir = tempDir(t)
  const drawing = drawingIn(dir)
  const out = join(dir, 'out')
  const lines = cli('render', drawing, '--svg', '-o', out).trim().split('\n')
  assert.deepEqual(lines, [1, 2, 3, 4].map((i) => join(out, `0${i}.svg`)))

  const json = cli('render', drawing, '--svg', '-o', out, '--theme', 'dark', '--json')
  assert.equal(json.trim().split('\n').length, 1)
  const { pages } = JSON.parse(json)
  assert.deepEqual(Object.keys(pages[0]), ['index', 'name', 'file', 'width', 'height'])
  assert.deepEqual(pages.map((p) => p.name), PAGES)
  assert.ok(pages.every((p) => existsSync(p.file) && Number.isInteger(p.width) && Number.isInteger(p.height)))
})

test('render without --svg still writes the PDF, and the SVG flags are checked', { skip, timeout: 180_000 }, (t) => {
  const dir = tempDir(t)
  const drawing = drawingIn(dir)
  assert.match(cli('render', drawing), /ki-cli\.pdf: 4 page\(s\)/)
  assert.ok(existsSync(join(dir, 'ki-cli.pdf')))
  for (const args of [['--json'], ['--theme', 'dark'], ['--svg'], ['--svg', '-o', dir, '--theme', 'sepia']]) {
    assert.throws(() => execFileSync('node', [join(ROOT, 'bin', 'ki-diagram.mjs'), 'render', drawing, ...args], { stdio: 'pipe' }), (err) => err.status === 2)
  }
})
