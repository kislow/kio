// Renders a drawing to PDF (one PDF page per tldraw page, each sized to its
// content) or to one SVG per page. tldraw runs in headless Chromium driven by the pinned Playwright;
// tldraw, its CSS and its fonts are served from node_modules on a made-up
// origin, and every other request is refused, so rendering never needs the
// network.
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { build } from 'esbuild'
import { chromium } from 'playwright-core'
import { readDrawing } from './drawing.mjs'
import { ROOT, browserCheck } from './requirements.mjs'

const ORIGIN = 'http://ki-diagram.invalid'
const TIMEOUT_MS = 120_000

// Opens the drawing in headless Chromium, exports every tldraw page to SVG and
// hands the exports and the browser page to `use`; PDF and SVG output share it.
async function withExport(drawingPath, { darkMode = false, env = process.env }, use) {
  const browser = browserCheck(env)
  if (!browser.ok) throw new Error(`${browser.detail}; fix: ${browser.fix}`)
  const drawing = readDrawing(drawingPath)
  const snapshot = { schema: drawing.raw.schema, store: Object.fromEntries(drawing.raw.records.map((r) => [r.id, r])) }
  const files = await pageFiles(snapshot)
  const title = drawing.raw.records.find((r) => r.typeName === 'document')?.name || 'drawing'

  const instance = await chromium.launch({ executablePath: env.KI_DIAGRAM_CHROME || undefined, timeout: 30_000 })
  try {
    const context = await instance.newContext({ viewport: { width: 1600, height: 1000 } })
    context.setDefaultTimeout(TIMEOUT_MS)
    await context.route('**/*', (route) => serve(route, files))
    const page = await context.newPage()
    const errors = []
    page.on('pageerror', (err) => errors.push(err.message))
    await page.goto(`${ORIGIN}/`)
    const pages = await page.evaluate((dark) => window.kiDiagram({ darkMode: dark }), darkMode).catch((err) => {
      throw new Error(`tldraw could not export ${drawingPath}: ${err.message}${errors.length ? `\n${errors.join('\n')}` : ''}`)
    })
    if (!pages.length) throw new Error(`${drawingPath} has no shapes to render`)
    return await use({ title, pages, page })
  } finally {
    await instance.close()
  }
}

export function renderPdf(drawingPath, pdfPath, env = process.env) {
  return withExport(drawingPath, { env }, async ({ title, pages, page }) => {
    await page.setContent(printDocument(title, pages))
    writeFileSync(pdfPath, await page.pdf({ preferCSSPageSize: true, printBackground: true }))
    return pages.map((p) => p.name)
  })
}

// One standalone SVG per tldraw page in `dir` as 01.svg, 02.svg, ...; older
// NN.svg files there go, so a page deleted from the drawing does not linger.
// tldraw inlines the fonts it uses into each SVG, so they need no network.
export function renderSvg(drawingPath, dir, { theme = 'light', env = process.env } = {}) {
  return withExport(drawingPath, { darkMode: theme === 'dark', env }, ({ pages }) => {
    mkdirSync(dir, { recursive: true })
    for (const f of readdirSync(dir)) if (/^\d{2,}\.svg$/.test(f)) rmSync(join(dir, f))
    return pages.map((p, i) => {
      const file = resolve(dir, `${String(i + 1).padStart(2, '0')}.svg`)
      writeFileSync(file, p.svg)
      return { index: i + 1, name: p.name, file, width: Math.ceil(p.width), height: Math.ceil(p.height) }
    })
  })
}

async function pageFiles(snapshot) {
  const bundle = await build({
    entryPoints: [join(ROOT, 'lib', 'page', 'entry.mjs')],
    bundle: true, write: false, format: 'iife', minify: true, logLevel: 'silent',
    define: { 'process.env.NODE_ENV': '"production"' },
  })
  const html = '<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="/tldraw.css">'
    + '<style>html,body,#root{margin:0;width:1600px;height:1000px}</style></head>'
    + '<body><div id="root"></div><script src="/page.js"></script></body></html>'
  return {
    '/': ['text/html', html],
    '/page.js': ['text/javascript', bundle.outputFiles[0].text],
    '/tldraw.css': ['text/css', readFileSync(join(ROOT, 'node_modules', 'tldraw', 'tldraw.css'))],
    '/snapshot.json': ['application/json', JSON.stringify(snapshot)],
  }
}

const TYPES = { '.woff2': 'font/woff2', '.svg': 'image/svg+xml', '.json': 'application/json', '.png': 'image/png' }

function serve(route, files) {
  const url = new URL(route.request().url())
  if (url.origin !== ORIGIN) return route.abort('blockedbyclient')
  const file = files[url.pathname]
  if (file) return route.fulfill({ contentType: file[0], body: file[1] })
  if (url.pathname.startsWith('/assets/') && !url.pathname.includes('..')) {
    const path = join(ROOT, 'node_modules', '@tldraw', 'assets', decodeURIComponent(url.pathname.slice('/assets/'.length)))
    try {
      return route.fulfill({ contentType: TYPES[path.slice(path.lastIndexOf('.'))] ?? 'application/octet-stream', body: readFileSync(path) })
    } catch {
      return route.fulfill({ status: 404 })
    }
  }
  return route.fulfill({ status: 404 })
}

// One printed page per drawing page, each at the size of its SVG.
function printDocument(title, pages) {
  const esc = (s) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])
  const css = pages.map((p, i) => `@page p${i}{size:${Math.ceil(p.width)}px ${Math.ceil(p.height)}px;margin:0}.p${i}{page:p${i};width:${Math.ceil(p.width)}px;height:${Math.ceil(p.height)}px}`).join('')
  const body = pages.map((p, i) => `<section class="p${i}">${p.svg}</section>`).join('')
  return `<!doctype html><html><head><meta charset="utf-8"><title>${esc(title)}</title><style>html,body{margin:0}section{overflow:hidden;break-after:page}section:last-child{break-after:auto}section svg{display:block}${css}</style></head><body>${body}</body></html>`
}
